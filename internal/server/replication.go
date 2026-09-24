package server

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"net"
	"os"
	"slices"
	"snugkv/internal/persistence"
	"strconv"
	"strings"
	"sync"
	"time"
)

type replicationRole uint8

const (
	replicationMaster replicationRole = iota
	replicationReplica
)

type replicationBacklogEntry struct {
	startOffset int64
	endOffset   int64
	payload     []byte
}

type replicationState struct {
	mu sync.RWMutex

	role replicationRole

	masterHost           string
	masterPort           int
	masterLinkStatus     string
	masterSyncInProgress bool

	connectedReplicas int
	runID             string
	offset            int64

	backlogActive      bool
	backlogSize        int64
	backlogBytes       int64
	backlogFirstOffset int64
	backlog            []replicationBacklogEntry

	nextReplicaID     uint64
	replicas          map[uint64]func([]byte) error
	replicaAckOffsets map[uint64]int64
	replicaAckTimes   map[uint64]time.Time

	followCancel      chan struct{}
	followDone        chan struct{}
	masterRunID       string
	masterRedisStream bool
}

type replicationSnapshot struct {
	role                 replicationRole
	masterHost           string
	masterPort           int
	masterLinkStatus     string
	masterSyncInProgress bool
	connectedReplicas    int
	runID                string
	offset               int64
	backlogActive        bool
	backlogSize          int64
	backlogBytes         int64
	backlogFirstOffset   int64
}

func (r *replicationState) snapshot() replicationSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return replicationSnapshot{
		role:                 r.role,
		masterHost:           r.masterHost,
		masterPort:           r.masterPort,
		masterLinkStatus:     r.masterLinkStatus,
		masterSyncInProgress: r.masterSyncInProgress,
		connectedReplicas:    r.connectedReplicas,
		runID:                r.runID,
		offset:               r.offset,
		backlogActive:        r.backlogActive,
		backlogSize:          r.backlogSize,
		backlogBytes:         r.backlogBytes,
		backlogFirstOffset:   r.backlogFirstOffset,
	}
}

func (r *replicationState) setReplica(host string, port int) {
	r.mu.Lock()
	if r.masterHost != host || r.masterPort != port {
		r.masterRunID = ""
		r.masterRedisStream = false
		r.offset = 0
	}
	r.role = replicationReplica
	r.masterHost = host
	r.masterPort = port
	r.masterLinkStatus = "down"
	r.masterSyncInProgress = true
	r.mu.Unlock()
}

func (r *replicationState) setReplicaConnected() {
	r.mu.Lock()
	if r.role == replicationReplica {
		r.masterLinkStatus = "up"
		r.masterSyncInProgress = false
	}
	r.mu.Unlock()
}

func (r *replicationState) setReplicaDisconnected() {
	r.mu.Lock()
	if r.role == replicationReplica {
		r.masterLinkStatus = "down"
		r.masterSyncInProgress = false
	}
	r.mu.Unlock()
}

func (r *replicationState) promote() {
	r.mu.Lock()
	r.role = replicationMaster
	r.masterHost = ""
	r.masterPort = 0
	r.masterLinkStatus = ""
	r.masterSyncInProgress = false
	r.masterRunID = ""
	r.masterRedisStream = false
	r.mu.Unlock()
}

func (r *replicationState) setConnectedReplicas(n int) {
	r.mu.Lock()
	r.connectedReplicas = n
	r.mu.Unlock()
}

func (r *replicationState) isReadOnlyReplica() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.role == replicationReplica
}

func (s *Server) replicationInfo() string {
	state := s.replication.snapshot()
	if state.role == replicationReplica {
		return fmt.Sprintf(
			"# Replication\r\n"+
				"role:slave\r\n"+
				"master_host:%s\r\n"+
				"master_port:%d\r\n"+
				"master_link_status:%s\r\n"+
				"master_sync_in_progress:%d\r\n"+
				"slave_read_only:1\r\n",
			state.masterHost,
			state.masterPort,
			state.masterLinkStatus,
			replicationBoolInt(state.masterSyncInProgress),
		)
	}
	return fmt.Sprintf(
		"# Replication\r\n"+
			"role:master\r\n"+
			"connected_slaves:%d\r\n"+
			"%s"+
			"master_replid:%s\r\n"+
			"master_repl_offset:%d\r\n"+
			"repl_backlog_active:%d\r\n"+
			"repl_backlog_size:%d\r\n"+
			"repl_backlog_first_byte_offset:%d\r\n"+
			"repl_backlog_histlen:%d\r\n",
		state.connectedReplicas,
		s.replication.replicaInfoLines(),
		state.runID,
		state.offset,
		replicationBoolInt(state.backlogActive),
		state.backlogSize,
		state.backlogFirstOffset,
		state.backlogBytes,
	)
}

func replicationBoolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (s *Server) replicationRoleReply() []byte {
	state := s.replication.snapshot()
	if state.role == replicationReplica {
		status := "connect"
		if state.masterLinkStatus == "up" {
			status = "connected"
		}
		return array(
			formatBulkString([]byte("slave")),
			formatBulkString([]byte(state.masterHost)),
			integer(int64(state.masterPort)),
			formatBulkString([]byte(status)),
			integer(state.offset),
		)
	}

	replicas := make([][]byte, 0, state.connectedReplicas)
	for i := 0; i < state.connectedReplicas; i++ {
		replicas = append(replicas, array(
			formatBulkString([]byte("127.0.0.1")),
			integer(0),
			integer(state.offset),
		))
	}
	return array(
		formatBulkString([]byte("master")),
		integer(state.offset),
		array(replicas...),
	)
}

func parseReplicaOf(args [][]byte) (detach bool, host string, port int, err error) {
	if len(args) != 3 {
		return false, "", 0, errors.New("ERR wrong number of arguments for 'replicaof' command")
	}
	if strings.EqualFold(string(args[1]), "NO") && strings.EqualFold(string(args[2]), "ONE") {
		return true, "", 0, nil
	}
	p, parseErr := strconv.Atoi(string(args[2]))
	if parseErr != nil || p <= 0 || p > 65535 {
		return false, "", 0, errors.New("ERR Invalid master port")
	}
	if len(args[1]) == 0 {
		return false, "", 0, errors.New("ERR Invalid master address")
	}
	return false, string(args[1]), p, nil
}

func newReplicationRunID() string {
	var raw [20]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return strings.Repeat("0", 40)
	}
	return hex.EncodeToString(raw[:])
}

func (r *replicationState) init() {
	r.mu.Lock()
	if r.runID == "" {
		r.runID = newReplicationRunID()
	}
	if r.replicas == nil {
		r.replicas = make(map[uint64]func([]byte) error)
	}
	if r.replicaAckOffsets == nil {
		r.replicaAckOffsets = make(map[uint64]int64)
	}
	if r.replicaAckTimes == nil {
		r.replicaAckTimes = make(map[uint64]time.Time)
	}
	if r.backlogSize == 0 {
		r.backlogSize = 1024 * 1024
	}
	if r.backlogFirstOffset == 0 {
		r.backlogFirstOffset = 1
	}
	r.mu.Unlock()
}

func (r *replicationState) ensureBacklogLocked() {
	if r.backlogSize == 0 {
		r.backlogSize = 1024 * 1024
	}
	if !r.backlogActive {
		r.backlogActive = true
		r.backlogFirstOffset = r.offset + 1
	}
}

func (r *replicationState) appendBacklogLocked(frame []byte, payload []byte) {
	r.ensureBacklogLocked()
	start := r.offset + 1
	end := r.offset + int64(len(frame))
	entry := replicationBacklogEntry{
		startOffset: start,
		endOffset:   end,
		payload:     append([]byte(nil), payload...),
	}
	r.backlog = append(r.backlog, entry)
	r.backlogBytes += int64(len(frame))
	r.offset = end
	for len(r.backlog) > 0 && r.backlogBytes > r.backlogSize {
		r.backlogBytes -= r.backlog[0].endOffset - r.backlog[0].startOffset + 1
		r.backlog = r.backlog[1:]
	}
	if len(r.backlog) > 0 {
		r.backlogFirstOffset = r.backlog[0].startOffset
	} else {
		r.backlogFirstOffset = r.offset + 1
	}
}

func (r *replicationState) partialSyncPayloadLocked(runID string, offset int64) ([][]byte, bool) {
	if !r.backlogActive || runID == "" || runID != r.runID {
		return nil, false
	}
	// Redis PSYNC offsets identify the next byte the replica needs.
	if offset > r.offset+1 {
		return nil, false
	}
	if offset < r.backlogFirstOffset {
		return nil, false
	}
	payloads := make([][]byte, 0)
	for _, entry := range r.backlog {
		if entry.endOffset >= offset {
			payloads = append(payloads, append([]byte(nil), entry.payload...))
		}
	}
	return payloads, true
}

func (r *replicationState) primaryHasReplicas() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.role == replicationMaster && (len(r.replicas) > 0 || r.backlogActive)
}

func (r *replicationState) registerReplica(write func([]byte) error) (uint64, string, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.replicas == nil {
		r.replicas = make(map[uint64]func([]byte) error)
	}
	if r.replicaAckOffsets == nil {
		r.replicaAckOffsets = make(map[uint64]int64)
	}
	if r.replicaAckTimes == nil {
		r.replicaAckTimes = make(map[uint64]time.Time)
	}
	r.nextReplicaID++
	id := r.nextReplicaID
	r.replicas[id] = write
	r.replicaAckOffsets[id] = 0
	r.replicaAckTimes[id] = time.Now()
	r.connectedReplicas = len(r.replicas)
	return id, r.runID, r.offset
}

func (r *replicationState) unregisterReplica(id uint64) {
	r.mu.Lock()
	delete(r.replicas, id)
	delete(r.replicaAckOffsets, id)
	delete(r.replicaAckTimes, id)
	r.connectedReplicas = len(r.replicas)
	r.mu.Unlock()
}

func (r *replicationState) acknowledgeReplica(id uint64, offset int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.replicas[id]; !ok {
		return
	}
	if current, ok := r.replicaAckOffsets[id]; !ok || offset > current {
		r.replicaAckOffsets[id] = offset
	}
	r.replicaAckTimes[id] = time.Now()
}

func (r *replicationState) replicaInfoLines() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.replicas) == 0 {
		return ""
	}
	ids := make([]uint64, 0, len(r.replicas))
	for id := range r.replicas {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	now := time.Now()
	var b strings.Builder
	for i, id := range ids {
		lag := int(now.Sub(r.replicaAckTimes[id]).Seconds())
		if lag < 0 {
			lag = 0
		}
		fmt.Fprintf(&b, "slave%d:ip=127.0.0.1,port=0,state=online,offset=%d,lag=%d\r\n", i, r.replicaAckOffsets[id], lag)
	}
	return b.String()
}

func encodeReplicationFrame(records []persistence.Record) ([]byte, error) {
	var buf bytes.Buffer
	if err := persistence.WriteFrame(&buf, records); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeReplicationFrame(frame []byte) ([]persistence.Record, error) {
	if len(frame) < 8 {
		return nil, errors.New("short replication frame")
	}
	n := int(binary.LittleEndian.Uint32(frame[:4]))
	if n < 0 || n > persistence.MaxFrameBytes || len(frame) != 8+n {
		return nil, errors.New("invalid replication frame length")
	}
	payload := frame[8:]
	if crc32.ChecksumIEEE(payload) != binary.LittleEndian.Uint32(frame[4:8]) {
		return nil, errors.New("replication checksum mismatch")
	}
	var records []persistence.Record
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&records); err != nil {
		return nil, err
	}
	return records, nil
}

func replicationBulk(payload []byte) []byte {
	head := []byte("$" + strconv.Itoa(len(payload)) + "\r\n")
	out := make([]byte, 0, len(head)+len(payload)+2)
	out = append(out, head...)
	out = append(out, payload...)
	out = append(out, '\r', '\n')
	return out
}

func (s *Server) publishReplication(records []persistence.Record) {
	if len(records) == 0 {
		return
	}
	frame, err := encodeReplicationFrame(records)
	if err != nil {
		return
	}
	payload := replicationBulk(frame)

	s.replication.mu.Lock()
	s.replication.appendBacklogLocked(frame, payload)
	targets := make(map[uint64]func([]byte) error, len(s.replication.replicas))
	for id, write := range s.replication.replicas {
		targets[id] = write
	}
	s.replication.mu.Unlock()

	for id, write := range targets {
		if err := write(payload); err != nil {
			s.replication.unregisterReplica(id)
		}
	}
}

func (s *Server) handlePSYNC(write func([]byte) error, requestedRunID string, requestedOffset int64) (uint64, error) {
	s.durableMu.Lock()
	defer s.durableMu.Unlock()

	// durableMu prevents a committed write from slipping between backlog
	// selection and replica registration.
	s.replication.mu.RLock()
	payloads, partial := s.replication.partialSyncPayloadLocked(requestedRunID, requestedOffset)
	s.replication.mu.RUnlock()
	if partial {
		id, _, _ := s.replication.registerReplica(write)
		ok := false
		defer func() {
			if !ok {
				s.replication.unregisterReplica(id)
			}
		}()
		if err := write([]byte("+CONTINUE\r\n")); err != nil {
			return 0, err
		}
		for _, payload := range payloads {
			if err := write(payload); err != nil {
				return 0, err
			}
		}
		ok = true
		return id, nil
	}

	records := s.store.Export(nil)
	full := make([]persistence.Record, 0, len(records)+1)
	full = append(full, persistence.Record{Reset: true})
	full = append(full, records...)
	frame, err := encodeReplicationFrame(full)
	if err != nil {
		return 0, err
	}

	id, runID, offset := s.replication.registerReplica(write)
	s.replication.mu.Lock()
	s.replication.ensureBacklogLocked()
	s.replication.mu.Unlock()

	ok := false
	defer func() {
		if !ok {
			s.replication.unregisterReplica(id)
		}
	}()

	header := []byte(fmt.Sprintf("+FULLRESYNC %s %d\r\n", runID, offset))
	if err := write(header); err != nil {
		return 0, err
	}
	if err := write(replicationBulk(frame)); err != nil {
		return 0, err
	}
	ok = true
	return id, nil
}

func readReplicationEOFDelimited(reader *bufio.Reader, marker []byte) ([]byte, error) {
	if len(marker) == 0 {
		return nil, errors.New("empty Redis EOF marker")
	}

	payload := make([]byte, 0, 64<<10)
	candidate := make([]byte, 0, len(marker))

	for {
		b, err := reader.ReadByte()
		if err != nil {
			return nil, err
		}
		candidate = append(candidate, b)

		for len(candidate) > 0 && !bytes.HasPrefix(marker, candidate) {
			payload = append(payload, candidate[0])
			candidate = candidate[1:]
			if len(payload) > persistence.MaxFrameBytes {
				return nil, errors.New("Redis EOF-framed snapshot exceeds limit")
			}
		}

		if len(candidate) == len(marker) {
			return payload, nil
		}
	}
}

func readReplicationSnapshot(reader *bufio.Reader) ([]byte, bool, error) {
	p, err := reader.ReadByte()
	if err != nil {
		return nil, false, err
	}
	if p != '$' {
		line, _ := reader.ReadString('\n')
		return nil, false, fmt.Errorf("unexpected replication snapshot %q", string(p)+line)
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, false, err
	}
	header := strings.TrimSpace(line)
	if strings.HasPrefix(header, "EOF:") {
		marker := []byte(strings.TrimPrefix(header, "EOF:"))
		if len(marker) != 40 {
			return nil, false, errors.New("invalid Redis EOF marker length")
		}
		payload, err := readReplicationEOFDelimited(reader, marker)
		if err != nil {
			return nil, false, err
		}
		if !isRedisRDBPayload(payload) {
			return nil, false, errors.New("Redis EOF-framed snapshot is not an RDB payload")
		}
		return payload, true, nil
	}

	n, err := strconv.Atoi(header)
	if err != nil || n < 0 || n > persistence.MaxFrameBytes {
		return nil, false, errors.New("invalid replication snapshot length")
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, false, err
	}
	if isRedisRDBPayload(payload) {
		// Redis replication's length-prefixed RDB transfer ends exactly after
		// the advertised bytes; the command stream begins immediately.
		return payload, true, nil
	}
	var crlf [2]byte
	if _, err := io.ReadFull(reader, crlf[:]); err != nil || crlf != [2]byte{'\r', '\n'} {
		return nil, false, errors.New("invalid replication snapshot terminator")
	}
	return payload, false, nil
}

func readReplicationRESP(reader *bufio.Reader) ([]byte, error) {
	p, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	if p != '$' {
		line, _ := reader.ReadString('\n')
		return nil, fmt.Errorf("unexpected replication frame %q", string(p)+line)
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 0 || n > persistence.MaxFrameBytes+8 {
		return nil, errors.New("invalid replication bulk length")
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	var crlf [2]byte
	if _, err := io.ReadFull(reader, crlf[:]); err != nil || crlf != [2]byte{'\r', '\n'} {
		return nil, errors.New("invalid replication bulk terminator")
	}
	return payload, nil
}

func authenticateReplicationUpstream(conn net.Conn, reader *bufio.Reader, username, password string) error {
	if password == "" {
		return nil
	}

	args := []string{"AUTH"}
	if username != "" {
		args = append(args, username)
	}
	args = append(args, password)

	if err := writeReplicationRESPCommand(conn, args...); err != nil {
		return err
	}

	line, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "+OK") {
		return nil
	}
	if strings.HasPrefix(line, "-") {
		return errors.New("replication upstream authentication failed")
	}
	return errors.New("invalid replication upstream AUTH response")
}

func writeReplicationRESPCommand(conn net.Conn, args ...string) error {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "*%d\r\n", len(args))
	for _, arg := range args {
		fmt.Fprintf(&buf, "$%d\r\n%s\r\n", len(arg), arg)
	}
	_, err := conn.Write(buf.Bytes())
	return err
}

func (s *Server) startReplicaFollow(host string, port int) {
	s.stopReplicaFollow()
	s.replication.setReplica(host, port)

	cancel := make(chan struct{})
	done := make(chan struct{})
	s.replication.mu.Lock()
	s.replication.followCancel = cancel
	s.replication.followDone = done
	s.replication.mu.Unlock()

	go func() {
		defer close(done)
		s.runReplicaFollow(host, port, cancel)
	}()
}

func (s *Server) stopReplicaFollow() {
	s.replication.mu.Lock()
	cancel := s.replication.followCancel
	done := s.replication.followDone
	s.replication.followCancel = nil
	s.replication.followDone = nil
	s.replication.mu.Unlock()
	if cancel != nil {
		close(cancel)
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *Server) dialReplicationUpstream(host string, port int) (net.Conn, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	if !s.replicationMasterTLS {
		return net.DialTimeout("tcp", addr, 2*time.Second)
	}

	caPEM, err := os.ReadFile(s.replicationMasterTLSCA)
	if err != nil {
		return nil, fmt.Errorf("replication TLS CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("replication TLS CA contains no valid certificates")
	}

	serverName := s.replicationMasterTLSSNI
	if serverName == "" {
		serverName = host
	}
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    roots,
		ServerName: serverName,
	}

	if s.replicationMasterTLSCert != "" {
		cert, err := tls.LoadX509KeyPair(s.replicationMasterTLSCert, s.replicationMasterTLSKey)
		if err != nil {
			return nil, fmt.Errorf("replication TLS client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	dialer := &net.Dialer{Timeout: 2 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
	if err != nil {
		return nil, fmt.Errorf("replication TLS handshake failed: %w", err)
	}
	return conn, nil
}

func (s *Server) runReplicaFollow(host string, port int, cancel <-chan struct{}) {
	for {
		select {
		case <-cancel:
			return
		default:
		}

		conn, err := s.dialReplicationUpstream(host, port)
		if err != nil {
			s.replication.setReplicaDisconnected()
			select {
			case <-cancel:
				return
			case <-time.After(250 * time.Millisecond):
				continue
			}
		}

		err = s.consumeReplicationConnection(conn, cancel)
		_ = conn.Close()
		s.replication.setReplicaDisconnected()
		if err == nil {
			return
		}
		log.Printf("replication upstream %s:%d: %v", host, port, err)
		select {
		case <-cancel:
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (s *Server) consumeReplicationConnection(conn net.Conn, cancel <-chan struct{}) error {
	s.replication.mu.RLock()
	requestedRunID := s.replication.masterRunID
	requestedOffset := s.replication.offset + 1
	s.replication.mu.RUnlock()
	if requestedRunID == "" {
		requestedRunID = "?"
		requestedOffset = -1
	}
	reader := bufio.NewReader(conn)

	if err := authenticateReplicationUpstream(
		conn,
		reader,
		s.replicationMasterUser,
		s.replicationMasterAuth,
	); err != nil {
		return err
	}

	// Redis uses EOF-delimited diskless full sync only when the replica advertises
	// the EOF capability. Older SnugKV primaries may reject pre-PSYNC REPLCONF;
	// either +OK or -ERR is safe to ignore before PSYNC.
	if err := writeReplicationRESPCommand(conn, "REPLCONF", "capa", "eof"); err != nil {
		return err
	}
	if _, err := reader.ReadString('\n'); err != nil {
		return err
	}

	if err := writeReplicationRESPCommand(conn, "PSYNC", requestedRunID, strconv.FormatInt(requestedOffset, 10)); err != nil {
		return err
	}

	line, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	for strings.TrimSpace(line) == "" {
		line, err = reader.ReadString('\n')
		if err != nil {
			return err
		}
	}

	redisStream := false
	if strings.HasPrefix(line, "+FULLRESYNC ") {
		parts := strings.Fields(line)
		if len(parts) < 3 {
			return errors.New("invalid FULLRESYNC response")
		}
		off, parseErr := strconv.ParseInt(parts[2], 10, 64)
		if parseErr != nil {
			return errors.New("invalid FULLRESYNC offset")
		}
		s.replication.mu.Lock()
		s.replication.masterRunID = parts[1]
		s.replication.offset = off
		s.replication.mu.Unlock()

		snapshot, isRedisRDB, err := readReplicationSnapshot(reader)
		if err != nil {
			return err
		}
		var records []persistence.Record
		if isRedisRDB {
			records, err = decodeRedisFullSyncRDB(snapshot)
			redisStream = true
		} else {
			records, err = decodeReplicationFrame(snapshot)
		}
		if err == nil {
			s.replication.mu.Lock()
			s.replication.masterRedisStream = redisStream
			s.replication.mu.Unlock()
		}
		if err != nil {
			return err
		}
		s.durableMu.Lock()
		err = s.store.Restore(records, true)
		s.durableMu.Unlock()
		if err != nil {
			return err
		}
	} else if strings.HasPrefix(line, "+CONTINUE") {
		s.replication.mu.RLock()
		redisStream = s.replication.masterRedisStream
		s.replication.mu.RUnlock()
	} else {
		return errors.New("master did not provide FULLRESYNC or CONTINUE")
	}

	s.replication.setReplicaConnected()

	var transaction [][][]byte
	var transactionBytes int64
	for {
		select {
		case <-cancel:
			return nil
		default:
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		if redisStream {
			args, streamBytes, err := readRedisReplicationCommand(reader)
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				s.replication.mu.RLock()
				ackOffset := s.replication.offset
				s.replication.mu.RUnlock()
				if writeErr := writeReplicationRESPCommand(conn, "REPLCONF", "ACK", strconv.FormatInt(ackOffset, 10)); writeErr != nil {
					return writeErr
				}
				continue
			}
			if err != nil {
				return err
			}
			if len(args) == 0 {
				continue
			}

			cmd := strings.ToUpper(string(args[0]))
			if transaction != nil {
				transactionBytes += streamBytes
				if cmd == "EXEC" {
					if err := s.applyRedisReplicationBatch(transaction); err != nil {
						return err
					}
					s.replication.mu.Lock()
					s.replication.offset += transactionBytes
					s.replication.mu.Unlock()
					transaction = nil
					transactionBytes = 0
				} else if cmd == "DISCARD" {
					s.replication.mu.Lock()
					s.replication.offset += transactionBytes
					s.replication.mu.Unlock()
					transaction = nil
					transactionBytes = 0
				} else {
					transaction = append(transaction, args)
				}
				continue
			}

			if cmd == "MULTI" {
				transaction = make([][][]byte, 0)
				transactionBytes = streamBytes
				continue
			}

			if err := s.applyRedisReplicationBatch([][][]byte{args}); err != nil {
				return err
			}
			s.replication.mu.Lock()
			s.replication.offset += streamBytes
			ackOffset := s.replication.offset
			s.replication.mu.Unlock()

			if cmd == "REPLCONF" && len(args) >= 3 && strings.EqualFold(string(args[1]), "GETACK") {
				if err := writeReplicationRESPCommand(conn, "REPLCONF", "ACK", strconv.FormatInt(ackOffset, 10)); err != nil {
					return err
				}
			}
			continue
		}

		if first, peekErr := reader.Peek(1); peekErr == nil && len(first) == 1 && first[0] == '*' {
			redisStream = true
			s.replication.mu.Lock()
			s.replication.masterRedisStream = true
			s.replication.mu.Unlock()
			continue
		}

		frame, err := readReplicationRESP(reader)
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			s.replication.mu.RLock()
			ackOffset := s.replication.offset
			s.replication.mu.RUnlock()
			if writeErr := writeReplicationRESPCommand(conn, "REPLCONF", "ACK", strconv.FormatInt(ackOffset, 10)); writeErr != nil {
				return writeErr
			}
			continue
		}
		if err != nil {
			return err
		}
		records, err := decodeReplicationFrame(frame)
		if err != nil {
			return err
		}
		s.durableMu.Lock()
		err = s.store.Restore(records, true)
		s.refreshWatchesLocked()
		s.durableMu.Unlock()
		if err != nil {
			return err
		}
		s.replication.mu.Lock()
		s.replication.offset += int64(len(frame))
		s.replication.mu.Unlock()
	}
}
