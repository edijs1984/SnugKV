package server

import (
	"bufio"
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
	"time"
)

type TCPServer struct {
	admin                    *TCPServer
	adminOnly, ownsOptimizer bool
	inputBytes, outputBytes  uint64
	listener                 net.Listener
	server                   *Server
	config                   config.Config
	mu                       sync.Mutex
	connections              map[net.Conn]struct{}
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
	s := &TCPServer{ownsOptimizer: true, listener: ln, server: New(store), config: c, connections: make(map[net.Conn]struct{}), done: make(chan struct{})}
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
	writer := &serializedResponseWriter{server: s, conn: conn}
	pubSession := newPubSubSession(s.server, func(response []byte) error {
		err := writer.write(response)
		if err != nil {
			_ = peer.Close()
		}
		return err
	})
	defer pubSession.close()
	txSession := newTransactionSession(s.server)
	defer txSession.close()

	decoder, _ := resp.NewDecoder(bufio.NewReader(conn), s.config.Limits())
	for {
		if pubSession.active() {
			// Pub/Sub subscriptions are long-lived. Message delivery is outbound,
			// so an ordinary request read timeout must not kill an idle subscriber.
			if err := conn.SetReadDeadline(time.Time{}); err != nil {
				return
			}
		} else if err := conn.SetReadDeadline(time.Now().Add(time.Duration(s.config.ReadTimeoutMS) * time.Millisecond)); err != nil {
			return
		}
		msg, err := decoder.ReadCommand()
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

		// RESP2 subscribed clients remain in Pub/Sub mode. This must run before
		// transaction handling so MULTI is rejected by the subscribed-mode rules.
		if pubSession.active() {
			if handled, quit, pubSubErr := s.server.executePubSubConnectionCommand(pubSession, msg); handled {
				if pubSubErr != nil && writer.write(errorResponse(pubSubErr)) != nil {
					return
				}
				if quit {
					return
				}
				continue
			}
		}

		if handled, txResponse, txErr := s.server.executeTransactionConnectionCommand(txSession, msg); handled {
			if txErr != nil {
				txResponse = errorResponse(txErr)
			}
			if writer.write(txResponse) != nil {
				return
			}
			continue
		}

		if handled, quit, pubSubErr := s.server.executePubSubConnectionCommand(pubSession, msg); handled {
			if pubSubErr != nil {
				if writer.write(errorResponse(pubSubErr)) != nil {
					return
				}
			}
			if quit {
				return
			}
			continue
		}

		var result []byte
		if isBlockingListCommand(msg) || isBlockingZSetCommand(msg) || isBlockingStreamCommand(msg) {
			disconnected, stopWatch := watchConnectionDisconnect(peer)
			result, err = s.server.ExecuteWithCancel(msg, disconnected)
			stopWatch()
			if errors.Is(err, errBlockingClientGone) {
				return
			}
		} else {
			result, err = s.server.Execute(msg)
		}
		if err != nil {
			result = errorResponse(err)
		}
		if err = writer.write(result); err != nil {
			return
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
	if !strings.HasPrefix(message, "ERR ") && !strings.HasPrefix(message, "NOPROTO ") && !strings.HasPrefix(message, "NOSCRIPT ") && !strings.HasPrefix(message, "OOM ") && !strings.HasPrefix(message, "WRONGTYPE ") && !strings.HasPrefix(message, "EXECABORT ") && !strings.HasPrefix(message, "INVALIDOBJ ") {
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
	if s.server.optimizer != nil {
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
