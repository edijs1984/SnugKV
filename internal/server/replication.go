package server

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"encoding/binary"
	"io"
	"net"
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

type replicationState struct {
	mu sync.RWMutex

	role replicationRole

	masterHost string
	masterPort int
	masterLinkStatus string
	masterSyncInProgress bool

	connectedReplicas int
	runID string
	offset int64

	nextReplicaID uint64
	replicas map[uint64]func([]byte) error

	followCancel chan struct{}
	followDone chan struct{}
}

type replicationSnapshot struct {
	role replicationRole
	masterHost string
	masterPort int
	masterLinkStatus string
	masterSyncInProgress bool
	connectedReplicas int
	runID string
	offset int64
}

func (r *replicationState) snapshot() replicationSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return replicationSnapshot{
		role: r.role,
		masterHost: r.masterHost,
		masterPort: r.masterPort,
		masterLinkStatus: r.masterLinkStatus,
		masterSyncInProgress: r.masterSyncInProgress,
		connectedReplicas: r.connectedReplicas,
		runID: r.runID,
		offset: r.offset,
	}
}

func (r *replicationState) setReplica(host string, port int) {
	r.mu.Lock()
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
			"master_replid:%s\r\n"+
			"master_repl_offset:%d\r\n",
		state.connectedReplicas,
		state.runID,
		state.offset,
	)
}

func replicationBoolInt(v bool) int {
	if v { return 1 }
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
	r.mu.Unlock()
}

func (r *replicationState) primaryHasReplicas() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.role == replicationMaster && len(r.replicas) > 0
}

func (r *replicationState) registerReplica(write func([]byte) error) (uint64, string, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.replicas == nil {
		r.replicas = make(map[uint64]func([]byte) error)
	}
	r.nextReplicaID++
	id := r.nextReplicaID
	r.replicas[id] = write
	r.connectedReplicas = len(r.replicas)
	return id, r.runID, r.offset
}

func (r *replicationState) unregisterReplica(id uint64) {
	r.mu.Lock()
	delete(r.replicas, id)
	r.connectedReplicas = len(r.replicas)
	r.mu.Unlock()
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

	s.replication.mu.RLock()
	targets := make(map[uint64]func([]byte) error, len(s.replication.replicas))
	for id, write := range s.replication.replicas {
		targets[id] = write
	}
	s.replication.mu.RUnlock()

	for id, write := range targets {
		if err := write(payload); err != nil {
			s.replication.unregisterReplica(id)
		}
	}

	s.replication.mu.Lock()
	s.replication.offset += int64(len(frame))
	s.replication.mu.Unlock()
}

func (s *Server) handlePSYNC(write func([]byte) error) (uint64, error) {
	s.durableMu.Lock()
	defer s.durableMu.Unlock()

	records := s.store.Export(nil)
	full := make([]persistence.Record, 0, len(records)+1)
	full = append(full, persistence.Record{Reset: true})
	full = append(full, records...)
	frame, err := encodeReplicationFrame(full)
	if err != nil {
		return 0, err
	}

	id, runID, offset := s.replication.registerReplica(write)
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
	if _, err := io.ReadFull(reader, crlf[:]); err != nil || crlf != [2]byte{'\r','\n'} {
		return nil, errors.New("invalid replication bulk terminator")
	}
	return payload, nil
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

func (s *Server) runReplicaFollow(host string, port int, cancel <-chan struct{}) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	for {
		select {
		case <-cancel:
			return
		default:
		}

		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
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
		select {
		case <-cancel:
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (s *Server) consumeReplicationConnection(conn net.Conn, cancel <-chan struct{}) error {
	if err := writeReplicationRESPCommand(conn, "PSYNC", "?", "-1"); err != nil {
		return err
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "+FULLRESYNC ") {
		return errors.New("master did not provide FULLRESYNC")
	}
	parts := strings.Fields(line)
	if len(parts) >= 3 {
		if off, parseErr := strconv.ParseInt(parts[2], 10, 64); parseErr == nil {
			s.replication.mu.Lock()
			s.replication.offset = off
			s.replication.mu.Unlock()
		}
	}

	frame, err := readReplicationRESP(reader)
	if err != nil {
		return err
	}
	records, err := decodeReplicationFrame(frame)
	if err != nil {
		return err
	}

	s.durableMu.Lock()
	err = s.store.Restore(records, true)
	s.durableMu.Unlock()
	if err != nil {
		return err
	}
	s.replication.setReplicaConnected()

	for {
		select {
		case <-cancel:
			return nil
		default:
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		frame, err := readReplicationRESP(reader)
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
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
