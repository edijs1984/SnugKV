package server

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type clientUnblockMode uint8

const (
	clientUnblockNone clientUnblockMode = iota
	clientUnblockTimeout
	clientUnblockError
)

type clientSession struct {
	mu sync.RWMutex

	id   uint64
	conn net.Conn

	remoteAddr string
	localAddr  string

	name    string
	libName string
	libVer  string

	protocol atomic.Int32

	createdAt time.Time
	lastSeen          atomic.Int64
	lastCmd           atomic.Pointer[string]
	replicationOffset atomic.Int64

	blocked     bool
	unblockCh   chan struct{}
	unblockMode clientUnblockMode

	tracking     clientTrackingState
	trackingPush func([]byte) error

	scriptDebugMode    scriptDebugMode
	scriptDebugRuntime *scriptDebugRuntime
}

var (
	clientCommandGET = "get"
	clientCommandSET = "set"
)

func newClientSession(
	id uint64,
	conn net.Conn,
	remoteAddr string,
	localAddr string,
) *clientSession {
	now := time.Now()

	client := &clientSession{
		id:         id,
		conn:       conn,
		remoteAddr: remoteAddr,
		localAddr:  localAddr,
		createdAt:  now,
	}
	client.protocol.Store(2)
	client.lastSeen.Store(now.UnixNano())
	return client
}

func (c *clientSession) touch(args [][]byte) time.Time {
	now := time.Now()
	if c == nil {
		return now
	}

	var cmd *string
	if len(args) > 0 {
		raw := args[0]
		if len(raw) == 3 &&
			raw[0]|32 == 103 &&
			raw[1]|32 == 101 &&
			raw[2]|32 == 116 {
			cmd = &clientCommandGET
		} else if len(raw) == 3 &&
			raw[0]|32 == 115 &&
			raw[1]|32 == 101 &&
			raw[2]|32 == 116 {
			cmd = &clientCommandSET
		} else {
			value := strings.ToLower(string(raw))
			if len(args) > 1 && strings.EqualFold(string(raw), "CLIENT") {
				value += "|" + strings.ToLower(string(args[1]))
			}
			cmd = &value
		}
	}

	c.lastSeen.Store(now.UnixNano())
	c.lastCmd.Store(cmd)
	return now
}

func (c *clientSession) setProtocol(protocol int) {
	c.protocol.Store(int32(protocol))
}

func (c *clientSession) protocolVersion() int {
	protocol := c.protocol.Load()
	if protocol == 0 {
		return 2
	}
	return int(protocol)
}

func (c *clientSession) setName(name string) {
	c.mu.Lock()
	c.name = name
	c.mu.Unlock()
}

func (c *clientSession) getName() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.name
}

func (c *clientSession) setLibName(value string) {
	c.mu.Lock()
	c.libName = value
	c.mu.Unlock()
}

func (c *clientSession) setLibVer(value string) {
	c.mu.Lock()
	c.libVer = value
	c.mu.Unlock()
}

func (c *clientSession) beginBlocking() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.blocked = true
	c.unblockMode = clientUnblockNone
	c.unblockCh = make(chan struct{})

	return c.unblockCh
}

func (c *clientSession) endBlocking() (clientUnblockMode, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	mode := c.unblockMode
	wasUnblocked := mode != clientUnblockNone

	c.blocked = false
	c.unblockCh = nil
	c.unblockMode = clientUnblockNone

	return mode, wasUnblocked
}

func (c *clientSession) requestUnblock(mode clientUnblockMode) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.blocked || c.unblockCh == nil || c.unblockMode != clientUnblockNone {
		return false
	}

	c.unblockMode = mode
	close(c.unblockCh)

	return true
}

type clientSnapshot struct {
	id uint64

	remoteAddr string
	localAddr  string

	name    string
	libName string
	libVer  string

	protocol int

	createdAt time.Time
	lastSeen  time.Time
	lastCmd   string

	blocked bool
}

func (c *clientSession) snapshot() clientSnapshot {
	lastSeen := time.Unix(0, c.lastSeen.Load())
	lastCmd := ""
	if value := c.lastCmd.Load(); value != nil {
		lastCmd = *value
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	return clientSnapshot{
		id:         c.id,
		remoteAddr: c.remoteAddr,
		localAddr:  c.localAddr,
		name:       c.name,
		libName:    c.libName,
		libVer:     c.libVer,
		protocol:   int(c.protocol.Load()),
		createdAt:  c.createdAt,
		lastSeen:   lastSeen,
		lastCmd:    lastCmd,
		blocked:    c.blocked,
	}
}

func (s *TCPServer) registerClient(client *clientSession) {
	if client == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.clients == nil {
		s.clients = make(map[uint64]*clientSession)
	}

	s.clients[client.id] = client
}

func (s *TCPServer) unregisterClient(id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.clients == nil {
		return
	}

	delete(s.clients, id)
}

func (s *TCPServer) clientByID(id uint64) *clientSession {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.clients == nil {
		return nil
	}

	return s.clients[id]
}

func (s *TCPServer) clientSnapshots() []clientSnapshot {
	s.mu.Lock()
	clients := make([]*clientSession, 0, len(s.clients))
	for _, client := range s.clients {
		clients = append(clients, client)
	}
	s.mu.Unlock()

	snapshots := make([]clientSnapshot, 0, len(clients))
	for _, client := range clients {
		snapshots = append(snapshots, client.snapshot())
	}

	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].id < snapshots[j].id
	})

	return snapshots
}

func (s *TCPServer) executeClientConnectionCommand(
	session *clientSession,
	args [][]byte,
) (bool, []byte, error) {
	if len(args) == 0 || !strings.EqualFold(string(args[0]), "CLIENT") {
		return false, nil, nil
	}

	if len(args) < 2 {
		return true, nil, errors.New(
			"ERR wrong number of arguments for 'client' command",
		)
	}

	rawSubcommand := string(args[1])
	subcommand := strings.ToUpper(rawSubcommand)

	switch subcommand {
	case "ID":
		if len(args) != 2 {
			return true, nil, errors.New(
				"ERR wrong number of arguments for 'client|id' command",
			)
		}

		return true, integer(int64(session.id)), nil

	case "SETNAME":
		if len(args) != 3 {
			return true, nil, errors.New(
				"ERR wrong number of arguments for 'client|setname' command",
			)
		}

		name := string(args[2])

		if strings.ContainsAny(name, " \t\r\n") {
			return true, nil, errors.New(
				"ERR Client names cannot contain spaces, newlines or special characters.",
			)
		}

		session.setName(name)
		return true, []byte("+OK\r\n"), nil

	case "GETNAME":
		if len(args) != 2 {
			return true, nil, errors.New(
				"ERR wrong number of arguments for 'client|getname' command",
			)
		}

		name := session.getName()
		if name == "" {
			return true, nullBulk(), nil
		}

		return true, formatBulkString([]byte(name)), nil

	case "SETINFO":
		if len(args) != 4 {
			return true, nil, errors.New(
				"ERR wrong number of arguments for 'client|setinfo' command",
			)
		}

		attr := strings.ToUpper(string(args[2]))
		value := string(args[3])

		switch attr {
		case "LIB-NAME":
			session.setLibName(value)

		case "LIB-VER":
			session.setLibVer(value)

		default:
			return true, nil, errors.New(
				"ERR Unrecognized option or bad number of args for CLIENT SETINFO",
			)
		}

		return true, []byte("+OK\r\n"), nil

	case "INFO":
		if len(args) != 2 {
			return true, nil, errors.New(
				"ERR wrong number of arguments for 'client|info' command",
			)
		}

		line := formatClientInfo(session.snapshot(), time.Now())
		return true, formatBulkString([]byte(line)), nil

	case "LIST":
		now := time.Now()
		snapshots := s.clientSnapshots()

		if len(args) == 2 {
			return true, formatClientListResponse(snapshots, now), nil
		}

		if len(args) >= 4 && strings.EqualFold(string(args[2]), "ID") {
			ids := make(map[uint64]struct{}, len(args)-3)

			for _, raw := range args[3:] {
				id, err := strconv.ParseUint(string(raw), 10, 64)
				if err != nil {
					return true, nil, errors.New(
						"ERR value is not an integer or out of range",
					)
				}

				ids[id] = struct{}{}
			}

			filtered := make([]clientSnapshot, 0, len(snapshots))

			for _, snapshot := range snapshots {
				if _, ok := ids[snapshot.id]; ok {
					filtered = append(filtered, snapshot)
				}
			}

			return true, formatClientListResponse(filtered, now), nil
		}

		if len(args) == 4 && strings.EqualFold(string(args[2]), "TYPE") {
			typeName := strings.ToUpper(string(args[3]))

			switch typeName {
			case "NORMAL":
				return true, formatClientListResponse(snapshots, now), nil
			default:
				return true, nil, errors.New(
					"ERR Unknown client type '" + string(args[3]) + "'",
				)
			}
		}

		return true, nil, errors.New("ERR syntax error")

	case "KILL":
		return s.executeClientKill(session, args)

	case "UNBLOCK":
		return s.executeClientUnblock(args)

	case "HELP":
		if len(args) != 2 {
			return true, nil, errors.New(
				"ERR wrong number of arguments for 'client|help' command",
			)
		}

		return true, clientHelpRESP(), nil

	default:
		return true, nil, errors.New(
			"ERR unknown subcommand '" +
				rawSubcommand +
				"'. Try CLIENT HELP.",
		)
	}
}

func (s *TCPServer) executeClientKill(
	current *clientSession,
	args [][]byte,
) (bool, []byte, error) {
	if len(args) == 2 {
		return true, nil, errors.New(
			"ERR wrong number of arguments for 'client|kill' command",
		)
	}

	if len(args) < 4 {
		return true, nil, errors.New(
			"ERR syntax error",
		)
	}

	if !strings.EqualFold(string(args[2]), "ID") {
		return true, nil, errors.New(
			"ERR syntax error",
		)
	}

	id, err := strconv.ParseUint(string(args[3]), 10, 64)
	if err != nil {
		return true, nil, errors.New(
			"ERR value is not an integer or out of range",
		)
	}

	skipMe := true

	if len(args) > 4 {
		if len(args) != 6 || !strings.EqualFold(string(args[4]), "SKIPME") {
			return true, nil, errors.New("ERR syntax error")
		}

		switch strings.ToUpper(string(args[5])) {
		case "YES":
			skipMe = true
		case "NO":
			skipMe = false
		default:
			return true, nil, errors.New("ERR syntax error")
		}
	}

	if skipMe && current != nil && current.id == id {
		return true, integer(0), nil
	}

	target := s.clientByID(id)
	if target == nil {
		return true, integer(0), nil
	}

	if target.conn == nil {
		return true, integer(0), nil
	}

	// For another client this immediately tears down the target connection.
	// Self-kill with SKIPME NO is deferred until after the command response has
	// had a chance to leave the socket.
	if current != nil && current.id == target.id {
		conn := target.conn
		go func() {
			time.Sleep(time.Millisecond)
			_ = conn.Close()
		}()
	} else {
		_ = target.conn.Close()
	}

	return true, integer(1), nil
}

func (s *TCPServer) executeClientUnblock(
	args [][]byte,
) (bool, []byte, error) {
	if len(args) != 3 && len(args) != 4 {
		return true, nil, errors.New(
			"ERR wrong number of arguments for 'client|unblock' command",
		)
	}

	id, err := strconv.ParseUint(string(args[2]), 10, 64)
	if err != nil {
		return true, nil, errors.New(
			"ERR value is not an integer or out of range",
		)
	}

	mode := clientUnblockTimeout

	if len(args) == 4 {
		switch strings.ToUpper(string(args[3])) {
		case "TIMEOUT":
			mode = clientUnblockTimeout
		case "ERROR":
			mode = clientUnblockError
		default:
			return true, nil, errors.New(
				"ERR CLIENT UNBLOCK reason should be TIMEOUT or ERROR",
			)
		}
	}

	target := s.clientByID(id)
	if target == nil {
		return true, integer(0), nil
	}

	if !target.requestUnblock(mode) {
		return true, integer(0), nil
	}

	return true, integer(1), nil
}

func formatClientListResponse(
	snapshots []clientSnapshot,
	now time.Time,
) []byte {
	var b strings.Builder

	for _, snapshot := range snapshots {
		b.WriteString(formatClientInfo(snapshot, now))
		b.WriteByte('\n')
	}

	return formatBulkString([]byte(b.String()))
}

func formatClientInfo(
	client clientSnapshot,
	now time.Time,
) string {
	age := int64(now.Sub(client.createdAt).Seconds())
	if age < 0 {
		age = 0
	}

	idle := int64(now.Sub(client.lastSeen).Seconds())
	if idle < 0 {
		idle = 0
	}

	flags := "N"
	if client.blocked {
		flags = "b"
	}

	name := escapeClientListToken(client.name)
	libName := escapeClientListToken(client.libName)
	libVer := escapeClientListToken(client.libVer)

	cmd := client.lastCmd
	if cmd == "" {
		cmd = "client|list"
	}

	return fmt.Sprintf(
		"id=%d addr=%s laddr=%s fd=-1 name=%s age=%d idle=%d flags=%s "+
			"db=0 sub=0 psub=0 ssub=0 multi=-1 qbuf=0 qbuf-free=0 "+
			"argv-mem=0 multi-mem=0 rbs=0 rbp=0 obl=0 oll=0 omem=0 "+
			"tot-mem=0 events=r cmd=%s user=default redir=-1 resp=%d "+
			"lib-name=%s lib-ver=%s",
		client.id,
		client.remoteAddr,
		client.localAddr,
		name,
		age,
		idle,
		flags,
		cmd,
		client.protocol,
		libName,
		libVer,
	)
}

func escapeClientListToken(value string) string {
	if value == "" {
		return ""
	}

	var b strings.Builder

	for _, r := range value {
		switch r {
		case ' ':
			b.WriteString(`\x20`)
		case '\n':
			b.WriteString(`\x0a`)
		case '\r':
			b.WriteString(`\x0d`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(r)
		}
	}

	return b.String()
}

func clientHelpRESP() []byte {
	lines := []string{
		"CLIENT <subcommand> [<arg> [value] [opt] ...]. Subcommands are:",
		"GETNAME",
		"    Return the name of the current connection.",
		"ID",
		"    Return the ID of the current connection.",
		"INFO",
		"    Return information about the current client connection.",
		"KILL ID <client-id> [SKIPME YES|NO]",
		"    Kill a client connection by ID.",
		"LIST",
		"    Return information about client connections.",
		"SETINFO <LIB-NAME|LIB-VER> <value>",
		"    Set client library metadata.",
		"SETNAME <name>",
		"    Assign a name to the current connection.",
		"UNBLOCK <client-id> [TIMEOUT|ERROR]",
		"    Unblock a client blocked in a blocking command.",
		"HELP",
		"    Prints this help.",
	}

	var b strings.Builder
	b.WriteByte('*')
	b.WriteString(strconv.Itoa(len(lines)))
	b.WriteString("\r\n")

	for _, line := range lines {
		b.WriteByte('+')
		b.WriteString(line)
		b.WriteString("\r\n")
	}

	return []byte(b.String())
}

func mergeClientCancel(
	disconnected <-chan struct{},
	unblocked <-chan struct{},
) (<-chan struct{}, func()) {
	merged := make(chan struct{})
	stop := make(chan struct{})
	var once sync.Once

	finish := func() {
		once.Do(func() {
			close(merged)
		})
	}

	go func() {
		select {
		case <-disconnected:
			finish()
		case <-unblocked:
			finish()
		case <-stop:
		}
	}()

	return merged, func() {
		select {
		case <-stop:
		default:
			close(stop)
		}
	}
}

func clientBlockingTimeoutResponse(args [][]byte) []byte {
	if len(args) == 0 {
		return nullBulk()
	}

	switch strings.ToUpper(string(args[0])) {
	case "BLPOP",
		"BRPOP",
		"BZPOPMIN",
		"BZPOPMAX",
		"BZMPOP",
		"XREAD",
		"XREADGROUP":
		return []byte("*-1\r\n")

	case "BLMOVE",
		"BRPOPLPUSH":
		return nullBulk()

	default:
		return nullBulk()
	}
}
