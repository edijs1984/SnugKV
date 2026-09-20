package server

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"log"
	"net"
	"runtime/debug"
	"snugkv/internal/config"
	"snugkv/internal/engine"
	"snugkv/internal/optimizer"
	"snugkv/internal/resp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type TCPServer struct {
	admin                    *TCPServer
	adminOnly, ownsOptimizer bool
	inputBytes, outputBytes  uint64
	nextClientID             uint64
	trackingClients          uint64
	optimizerMaintainTicks   uint64
	optimizerDroppedSeen     uint64
	listener                 net.Listener
	server                   *Server
	config                   config.Config
	mu                       sync.Mutex
	connections              map[net.Conn]struct{}
	clients                  map[uint64]*clientSession
	closing                  bool
	wg                       sync.WaitGroup
	closeOnce                sync.Once
	done                     chan struct{}
}

func Listen(addr string, store *engine.Store) (*TCPServer, error) {
	c := config.Default()
	c.ListenAddr = addr
	return ListenWithConfig(c, store)
}
func ListenWithConfig(c config.Config, store *engine.Store) (*TCPServer, error) {
	return ListenWithJournal(c, store, nil)
}
func ListenWithJournal(c config.Config, store *engine.Store, journal Journal) (*TCPServer, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", c.ListenAddr)
	if err != nil {
		return nil, err
	}
	s := &TCPServer{ownsOptimizer: true, listener: ln, server: New(store), config: c, connections: make(map[net.Conn]struct{}), clients: make(map[uint64]*clientSession), done: make(chan struct{})}

	s.server.configAppendFsync = c.Fsync
	s.server.configACLFile = c.ACLFile

	// Redis loads the configured ACL file during startup. A configured ACL
	// file is authoritative: if it cannot be read or parsed, startup must fail
	// rather than silently falling back to the default ACL.
	if c.ACLFile != "" {
		if err := s.server.acl.LoadFile(c.ACLFile); err != nil {
			_ = ln.Close()
			return nil, err
		}
	}

	// CONFIG SET appendfsync changes the real persistence policy when
	// the configured journal supports runtime policy mutation.
	if configurable, ok := journal.(interface {
		SetPolicy(string) error
		Policy() string
	}); ok {
		s.server.configAppendFsync = configurable.Policy()

		s.server.configSetAppendFsync = func(policy string) error {
			if err := configurable.SetPolicy(policy); err != nil {
				return err
			}

			s.server.configMu.Lock()
			s.server.configAppendFsync = configurable.Policy()
			s.server.configMu.Unlock()

			return nil
		}
	} else {
		// Redis allows appendfsync to be changed even while appendonly
		// is disabled. Retain the effective runtime value in that case.
		s.server.configSetAppendFsync = func(policy string) error {
			s.server.configMu.Lock()
			s.server.configAppendFsync = policy
			s.server.configMu.Unlock()

			return nil
		}
	}
	s.server.configAppendOnly = c.AOFPath != ""

	s.server.configRewrite = func() error {
		s.mu.Lock()
		effective := s.config
		s.mu.Unlock()

		// Pull values from the components that actually enforce mutable
		// CONFIG settings instead of trusting a stale startup copy.
		effective.MaxMemory = s.server.store.MaxMemory()

		s.server.configMu.RLock()
		effective.Fsync = s.server.configAppendFsync
		s.server.configMu.RUnlock()

		effective.EvictionPolicy = s.server.eviction

		return config.Rewrite(effective)
	}

	s.server.configGetMaxClients = func() int {
		s.mu.Lock()
		defer s.mu.Unlock()

		return s.config.MaxConnections
	}

	s.server.configSetMaxClients = func(max int) {
		s.mu.Lock()
		s.config.MaxConnections = max
		s.mu.Unlock()
	}

	if c.Encoding {
		opt, err := optimizer.New(store, optimizer.Default())
		if err != nil {
			ln.Close()
			return nil, err
		}
		s.server.optimizer = opt
	}
	s.server.eviction = c.EvictionPolicy
	s.server.SetJournal(journal)
	s.wg.Add(1)
	go s.serve()
	return s, nil
}
func (s *TCPServer) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		child := s.admin
		s.server.CancelBlocking()
		s.server.CancelBlockingZSets()
		s.server.CancelBlockingStreams()
		s.listener.Close()
		for conn := range s.connections {
			conn.Close()
		}
		s.mu.Unlock()
		if child != nil {
			child.Close()
		}
		s.wg.Wait()
		if s.ownsOptimizer && s.server.optimizer != nil {
			s.server.optimizer.Close()
		}
		close(s.done)
	})
	<-s.done
	return nil
}
func (s *TCPServer) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closing || len(s.connections) >= s.config.MaxConnections {
			s.mu.Unlock()
			conn.Close()
			continue
		}
		s.connections[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			defer func() {
				conn.Close()
				s.mu.Lock()
				delete(s.connections, conn)
				s.mu.Unlock()
			}()
			defer recoverConnectionPanic()

			counted := countedConn{
				Conn:   conn,
				input:  &s.inputBytes,
				output: &s.outputBytes,
			}
			s.handleConnRaw(counted, conn)
		}()
	}
}
func recoverConnectionPanic() {
	if recovered := recover(); recovered != nil {
		log.Printf(
			"recovered panic while handling client connection: %v\n%s",
			recovered,
			debug.Stack(),
		)
	}
}

func (s *TCPServer) handleConn(conn net.Conn) {
	s.handleConnRaw(conn, conn)
}

func (s *TCPServer) handleConnRaw(conn net.Conn, peer net.Conn) {
	// Pub/Sub delivery can write from a publisher's goroutine while this
	// connection goroutine is blocked reading the next subscriber command.
	// Serialize complete responses so partial socket writes cannot interleave.
	writer := newSerializedResponseWriter(s, conn)
	var getScratch []byte
	var getKeyScratch []byte
	const maxRetainedGetScratch = 64 << 10
	const maxRetainedGetKeyScratch = 64 << 10

	clientID := atomic.AddUint64(
		&s.nextClientID,
		1,
	)

	clientSession := newClientSession(
		clientID,
		peer,
		peer.RemoteAddr().String(),
		peer.LocalAddr().String(),
	)

	clientSession.mu.Lock()
	clientSession.trackingPush = writer.write
	clientSession.mu.Unlock()

	s.registerClient(clientSession)
	defer func() {
		if clientSession.trackingIsEnabled() {
			atomic.AddUint64(&s.trackingClients, ^uint64(0))
		}
		s.unregisterClient(clientSession.id)
	}()
	defer writer.flush()
	defer clientSession.closeScriptDebugRuntime()

	pubSession := newPubSubSession(
		s.server,
		func(response []byte) error {
			if clientSession.protocolVersion() == 3 {
				response = resp3PubSubPush(response)
			}

			err := writer.write(response)
			if err != nil {
				_ = peer.Close()
			}

			return err
		},
	)
	defer pubSession.close()

	authSession := newAuthSession(
		s.server.acl,
	)

	txSession := newTransactionSession(
		s.server,
	)
	txSession.auth = authSession
	defer txSession.close()

	writeProtocol := func(
		command [][]byte,
		response []byte,
	) error {
		if clientSession.protocolVersion() == 3 {
			response = resp3AdaptCommand(
				command,
				response,
			)
		}

		return writer.writeBuffered(response)
	}

	reader := bufio.NewReaderSize(conn, 256<<10)
	decoder, _ := resp.NewDecoder(reader, s.config.Limits())
	for {
		// If no more request bytes are already buffered, flushing here avoids
		// waiting for the next client command while still allowing an existing
		// pipeline to accumulate responses into one socket write.
		if reader.Buffered() == 0 {
			if err := writer.flush(); err != nil {
				return
			}
		}
		if reader.Buffered() == 0 {
			if pubSession.active() {
				// Pub/Sub subscriptions are long-lived. Message delivery is outbound,
				// so an ordinary request read timeout must not kill an idle subscriber.
				if err := conn.SetReadDeadline(time.Time{}); err != nil {
					return
				}
			} else if err := conn.SetReadDeadline(time.Now().Add(time.Duration(s.config.ReadTimeoutMS) * time.Millisecond)); err != nil {
				return
			}
		}
		var borrowedGET [2][]byte
		var msg [][]byte
		borrowedKey, borrowed, borrowErr := decoder.ReadBufferedGET(getKeyScratch)
		if borrowErr != nil {
			return
		}
		if borrowed {
			borrowedGET[0] = []byte("GET")
			borrowedGET[1] = borrowedKey
			msg = borrowedGET[:]
			if cap(borrowedKey) <= maxRetainedGetKeyScratch {
				getKeyScratch = borrowedKey[:0]
			} else {
				getKeyScratch = nil
			}
		}
		var err error
		if !borrowed {
			msg, err = decoder.ReadCommand()
		}
		if err != nil {
			if err == io.EOF {
				return
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				return
			}
			log.Printf("RESP decode error: %v", err)
			_ = writer.write([]byte("-ERR invalid RESP\r\n"))
			return
		}
		requestNow := clientSession.touch(msg)

		if !borrowed && len(msg) > 0 &&
			strings.EqualFold(string(msg[0]), "HELLO") {
			response, helloErr :=
				s.executeHelloConnectionCommand(
					clientSession,
					authSession,
					msg,
				)

			if helloErr != nil {
				response = errorResponse(helloErr)
			}

			if writeProtocol(msg, response) != nil {
				return
			}

			continue
		}

		if !borrowed && len(msg) > 0 && strings.EqualFold(string(msg[0]), "AUTH") {
			response, authErr := s.server.executeAUTH(authSession, msg)

			if authErr != nil {
				if strings.HasPrefix(
					authErr.Error(),
					"WRONGPASS ",
				) {
					username := "default"

					if len(msg) >= 3 {
						username = string(msg[1])
					}

					s.server.aclLog.Add(
						"auth",
						"AUTH",
						username,
						aclLogClientInfo(
							clientSession,
							authSession,
						),
					)
				}

				response = errorResponse(authErr)
			}

			if writeProtocol(msg, response) != nil {
				return
			}

			continue
		}

		if !borrowed {
			if handled, debugResponse, debugErr :=
				s.executeScriptDebugCommand(
					clientSession,
					authSession,
					msg,
				); handled {
			if errors.Is(debugErr, errScriptDebugCloseAfterReply) {
				if writer.write(debugResponse) != nil {
					return
				}
				return
			}
			if debugErr != nil {
				debugResponse = errorResponse(debugErr)
			}
				if writer.write(debugResponse) != nil {
					return
				}
				continue
			}
		}

		if authErr := s.server.authorizeConnectionCommand(authSession, msg); authErr != nil {
			// Redis marks a MULTI transaction dirty when a command cannot be
			// queued because ACL authorization failed. EXEC must subsequently
			// abort the entire transaction.
			txSession.markACLFailure()

			message := authErr.Error()

			switch {
			case strings.HasPrefix(message, "NOPERM User "):
				s.server.aclLog.Add(
					"command",
					aclCanonicalCommand(msg),
					authSession.username,
					aclLogClientInfo(
						clientSession,
						authSession,
					),
				)

			case message == "NOPERM No permissions to access a key":
				s.server.aclLog.Add(
					"key",
					s.server.firstDeniedACLKey(
						authSession.username,
						msg,
					),
					authSession.username,
					aclLogClientInfo(
						clientSession,
						authSession,
					),
				)

			case message == "NOPERM No permissions to access a channel":
				s.server.aclLog.Add(
					"channel",
					s.server.firstDeniedACLChannel(
						authSession.username,
						msg,
					),
					authSession.username,
					aclLogClientInfo(
						clientSession,
						authSession,
					),
				)
			}

			if writeProtocol(msg, errorResponse(authErr)) != nil {
				return
			}

			continue
		}

		// ReadBufferedGET has already validated the complete command as exactly
		// GET with two bulk arguments. On the ordinary data listener, run the
		// authorized GET path before generic ACL/admin command classification.
		// Authorization above is unchanged; RESP2 Pub/Sub and MULTI semantics
		// are still checked before the read. Admin listeners keep the generic
		// gate below because GET is intentionally unavailable there.
		if borrowed && !s.adminOnly {
			if clientSession.protocolVersion() == 2 && pubSession.active() {
				if handled, quit, pubSubErr := s.server.executePubSubConnectionCommand(pubSession, msg); handled {
					if pubSubErr != nil {
						if writeProtocol(msg, errorResponse(pubSubErr)) != nil {
							return
						}
					}
					if quit {
						return
					}
					continue
				}
			}

			// A buffered GET cannot itself be a transaction-control command.
			// Only consult the transaction dispatcher when this connection is
			// already inside MULTI, where GET must be queued.
			if txSession.multi {
				if handled, txResponse, txErr := s.server.executeTransactionConnectionCommand(txSession, msg); handled {
					if txErr != nil {
						txResponse = errorResponse(txErr)
					}
					if writeProtocol(msg, txResponse) != nil {
						return
					}
					continue
				}
			}

			if value, found, handled, fastErr := s.server.executeAuthorizedConcurrentKnownGetIntoAt(msg[1], getScratch, requestNow); handled {
				if fastErr != nil {
					if writeProtocol(msg, errorResponse(fastErr)) != nil {
						return
					}
					continue
				}
				if found {
					if writer.writeBulkBuffered(value) != nil {
						return
					}
					if cap(value) <= maxRetainedGetScratch {
						getScratch = value[:0]
					} else {
						getScratch = nil
					}
				} else if writeProtocol(msg, nullBulk()) != nil {
					return
				}
				s.trackCommandRead(clientSession, msg)
				continue
			}
		}

		if len(msg) > 0 && strings.EqualFold(string(msg[0]), "ACL") {
			response, aclErr := s.server.executeACL(authSession, msg)

			if aclErr != nil {
				response = errorResponse(aclErr)
			}

			if writeProtocol(msg, response) != nil {
				return
			}

			continue
		}

		if s.adminOnly && !adminAllowed(msg) {
			if writer.write([]byte("-ERR command is unavailable on admin listener\r\n")) != nil {
				return
			}
			continue
		}
		if !s.adminOnly && s.admin != nil && isAdminCommand(msg) {
			if writer.write([]byte("-ERR use the admin listener\r\n")) != nil {
				return
			}
			continue
		}
		if isAdminCommand(msg) {
			host, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
			ip := net.ParseIP(host)
			if ip == nil || !ip.IsLoopback() {
				if writer.write([]byte("-ERR administrative commands require loopback access\r\n")) != nil {
					return
				}
				continue
			}
		}

		if len(msg) == 2 && bytes.EqualFold(msg[0], []byte("GET")) {
			// Preserve RESP2 subscribed-mode semantics before ordinary GET
			// execution. RESP3 subscribers may issue normal commands.
			if clientSession.protocolVersion() == 2 && pubSession.active() {
				if handled, quit, pubSubErr := s.server.executePubSubConnectionCommand(pubSession, msg); handled {
					if pubSubErr != nil {
						if writeProtocol(msg, errorResponse(pubSubErr)) != nil {
							return
						}
					}
					if quit {
						return
					}
					continue
				}
			}

			// MULTI must queue GET instead of executing it immediately.
			if handled, txResponse, txErr := s.server.executeTransactionConnectionCommand(txSession, msg); handled {
				if txErr != nil {
					txResponse = errorResponse(txErr)
				}
				if writeProtocol(msg, txResponse) != nil {
					return
				}
				continue
			}

			if value, found, handled, fastErr := s.server.executeAuthorizedConcurrentKnownGetIntoAt(msg[1], getScratch, requestNow); handled {
				if fastErr != nil {
					if writeProtocol(msg, errorResponse(fastErr)) != nil {
						return
					}
					continue
				}
				if found {
					if writer.writeBulkBuffered(value) != nil {
						return
					}
					if cap(value) <= maxRetainedGetScratch {
						getScratch = value[:0]
					} else {
						getScratch = nil
					}
				} else if writeProtocol(msg, nullBulk()) != nil {
					return
				}
				s.trackCommandRead(clientSession, msg)
				continue
			}
		}

		if handled, debugResponse, debugErr :=
			s.executeScriptDebugControl(
				clientSession,
				msg,
			); handled {
			if debugErr != nil {
				debugResponse = errorResponse(debugErr)
			}
			if writeProtocol(msg, debugResponse) != nil {
				return
			}
			continue
		}

		if handled, debugResponse, debugErr :=
			s.beginScriptDebugEval(
				clientSession,
				authSession,
				msg,
			); handled {
			if debugErr != nil {
				debugResponse = errorResponse(debugErr)
			}
			if writer.write(debugResponse) != nil {
				return
			}
			continue
		}

		if handled, trackingResponse, trackingErr :=
			s.executeClientTracking(
				clientSession,
				msg,
			); handled {
			if trackingErr != nil {
				trackingResponse = errorResponse(trackingErr)
			}
			if writeProtocol(msg, trackingResponse) != nil {
				return
			}
			continue
		}

		if handled, clientResponse, clientErr := s.executeClientConnectionCommand(clientSession, msg); handled {
			if clientErr != nil {
				clientResponse = errorResponse(clientErr)
			}
			if writeProtocol(msg, clientResponse) != nil {
				return
			}
			continue
		}

		// RESP2 subscribed clients remain in Pub/Sub mode.
		// RESP3 subscribers may continue executing ordinary commands while
		// subscriptions remain active.
		if clientSession.protocolVersion() == 2 &&
			pubSession.active() {
			if handled, quit, pubSubErr :=
				s.server.executePubSubConnectionCommand(
					pubSession,
					msg,
				); handled {
				if pubSubErr != nil {
					if writeProtocol(
						msg,
						errorResponse(pubSubErr),
					) != nil {
						return
					}
				}

				if quit {
					return
				}

				continue
			}
		}

		if handled, txResponse, txErr :=
			s.server.executeTransactionConnectionCommand(
				txSession,
				msg,
			); handled {
			if txErr != nil {
				txResponse = errorResponse(txErr)
			}

			if writeProtocol(
				msg,
				txResponse,
			) != nil {
				return
			}

			continue
		}

		// RESP2 always passes Pub/Sub commands through this dispatcher.
		// RESP3 only does so for the actual Pub/Sub connection commands;
		// ordinary commands continue through normal execution.
		if clientSession.protocolVersion() == 2 ||
			isPubSubConnectionCommand(msg) {
			if handled, quit, pubSubErr :=
				s.server.executePubSubConnectionCommand(
					pubSession,
					msg,
				); handled {
				if pubSubErr != nil {
					if writeProtocol(
						msg,
						errorResponse(pubSubErr),
					) != nil {
						return
					}
				}

				if quit {
					return
				}

				continue
			}
		}

		if handled, fastErr := s.server.executeAuthorizedConcurrentRawGet(
			msg,
			writer.writeBulkBuffered,
		); handled {
			if fastErr != nil {
				return
			}
			s.trackCommandRead(clientSession, msg)
			continue
		}

		if value, found, handled, fastErr := s.server.executeAuthorizedConcurrentGetInto(msg, getScratch); handled {
			if fastErr != nil {
				if writeProtocol(msg, errorResponse(fastErr)) != nil {
					return
				}
				continue
			}
			if found {
				if writer.writeBulkBuffered(value) != nil {
					return
				}
				if cap(value) <= maxRetainedGetScratch {
					getScratch = value[:0]
				} else {
					getScratch = nil
				}
			} else if writeProtocol(msg, nullBulk()) != nil {
				return
			}
			s.trackCommandRead(clientSession, msg)
			continue
		}

		if result, handled, fastErr := s.server.executeAuthorizedConcurrentSet(msg); handled {
			if fastErr != nil {
				result = errorResponse(fastErr)
			}
			commandSucceeded := fastErr == nil
			if writeProtocol(msg, result) != nil {
				return
			}
			if commandSucceeded {
				s.invalidateTrackingKeys(clientSession, msg)
			}
			continue
		}

		var result []byte
		if isBlockingListCommand(msg) || isBlockingZSetCommand(msg) || isBlockingStreamCommand(msg) {
			disconnected, stopWatch := watchConnectionDisconnect(peer)
			unblock := clientSession.beginBlocking()

			cancel, stopMerge := mergeClientCancel(disconnected, unblock)
			result, err = s.server.executeWithCancelForSession(
				msg,
				cancel,
				authSession,
			)

			stopMerge()
			stopWatch()

			unblockMode, wasUnblocked := clientSession.endBlocking()

			if errors.Is(err, errBlockingClientGone) && wasUnblocked {
				switch unblockMode {
				case clientUnblockError:
					result = []byte("-UNBLOCKED client unblocked via CLIENT UNBLOCK\r\n")
				default:
					result = clientBlockingTimeoutResponse(msg)
				}
				err = nil
			} else if errors.Is(err, errBlockingClientGone) {
				return
			}
		} else {
			result, err = s.server.executeForSession(
				msg,
				authSession,
			)
		}
		if err != nil {
			result = errorResponse(err)
		}
		commandSucceeded := err == nil
		if err = writeProtocol(msg, result); err != nil {
			return
		}
		if commandSucceeded {
			s.trackCommandRead(clientSession, msg)
			s.invalidateTrackingKeys(clientSession, msg)
		}
		if len(msg) == 1 && strings.EqualFold(string(msg[0]), "QUIT") {
			return
		}
	}
}
func (s *TCPServer) write(conn net.Conn, response []byte) error {
	return writeWithTimeout(conn, response, time.Duration(s.config.WriteTimeoutMS)*time.Millisecond)
}
func errorResponse(err error) []byte {
	message := strings.TrimSpace(err.Error())
	message = strings.NewReplacer("\r", " ", "\n", " ").Replace(message)
	if !strings.HasPrefix(message, "ERR ") &&
		!strings.HasPrefix(message, "NOPROTO ") &&
		!strings.HasPrefix(message, "NOSCRIPT ") &&
		!strings.HasPrefix(message, "OOM ") &&
		!strings.HasPrefix(message, "WRONGTYPE ") &&
		!strings.HasPrefix(message, "EXECABORT ") &&
		!strings.HasPrefix(message, "INVALIDOBJ ") &&
		!strings.HasPrefix(message, "NOAUTH ") &&
		!strings.HasPrefix(message, "WRONGPASS ") &&
		!strings.HasPrefix(message, "NOPERM ") &&
		!strings.HasPrefix(message, "BUSYGROUP ") &&
		!strings.HasPrefix(message, "BUSYKEY ") &&
		!strings.HasPrefix(message, "NOGROUP ") {
		message = "ERR " + message
	}
	return []byte("-" + message + "\r\n")
}
func writeResponse(conn net.Conn, response []byte) error {
	return writeWithTimeout(conn, response, 30*time.Second)
}
func writeWithTimeout(conn net.Conn, response []byte, timeout time.Duration) error {
	if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	for len(response) > 0 {
		n, err := conn.Write(response)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(response) {
			return io.ErrShortWrite
		}
		response = response[n:]
	}
	return nil
}

func (s *TCPServer) OptimizeSample() {
	if s.server.optimizer == nil {
		return
	}

	stats := s.server.optimizer.Stats()

	// Fresh queue drops are the signal for aggressive recovery sampling.
	// Sample a large bounded burst only when drops have actually increased.
	if stats.Dropped > s.optimizerDroppedSeen {
		s.optimizerDroppedSeen = stats.Dropped
		s.server.optimizer.Sample(4096)
		return
	}

	// When direct write-time enqueue is keeping up, a large 100ms sampling burst
	// just rechecks already-optimized keys. Keep a small periodic discovery pass
	// for uncommon mutation paths that do not enqueue directly.
	s.optimizerMaintainTicks++
	if s.optimizerMaintainTicks%100 == 0 {
		s.server.optimizer.Sample(256)
	}
}

// Maintain performs semantic expiration cleanup under the same serialization
// mutex used by commands and EXEC, so an expiry sweep cannot interleave halfway
// through a transaction. Optimizer rewrites are logical no-ops and may remain
// asynchronous.
func (s *TCPServer) Maintain() {
	s.server.durableMu.Lock()
	s.server.refreshWatchesLocked()
	s.server.store.CleanupExpiredLimit(1024)
	s.server.refreshWatchesLocked()
	s.server.durableMu.Unlock()
	s.OptimizeSample()
}