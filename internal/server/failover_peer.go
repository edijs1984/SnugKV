package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

type failoverPeerState struct {
	NodeID      string `json:"node_id"`
	Role        string `json:"role"`
	MasterDown  bool   `json:"master_down"`
	Offset      int64  `json:"offset"`
	Priority    int    `json:"priority"`
	MasterRunID string `json:"master_run_id,omitempty"`
	Term        uint64 `json:"term"`
}

type failoverVoteReply struct {
	Term    uint64 `json:"term"`
	Granted bool   `json:"granted"`
}

func (s *Server) localFailoverState(now time.Time) failoverPeerState {
	s.replication.mu.RLock()
	role := s.replication.role
	nodeID := s.replication.runID
	offset := s.replication.offset
	masterRunID := s.replication.masterRunID
	masterDown := role == replicationReplica &&
		s.replication.masterLinkStatus == "down" &&
		!s.replication.masterDownSince.IsZero() &&
		s.autoFailoverTimeout > 0 &&
		now.Sub(s.replication.masterDownSince) >= s.autoFailoverTimeout
	s.replication.mu.RUnlock()

	roleName := "master"
	if role == replicationReplica {
		roleName = "replica"
	}
	s.failoverVoteMu.Lock()
	term := s.failoverTerm
	s.failoverVoteMu.Unlock()
	return failoverPeerState{
		NodeID:      nodeID,
		Role:        roleName,
		MasterDown:  masterDown,
		Offset:      offset,
		Priority:    s.failoverPriority,
		MasterRunID: masterRunID,
		Term:        term,
	}
}

func (s *Server) failoverStateJSON(now time.Time) ([]byte, error) {
	return json.Marshal(s.localFailoverState(now))
}

func readRESPBulk(reader *bufio.Reader) ([]byte, error) {
	prefix, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	if prefix == '-' {
		line, _ := reader.ReadString('\n')
		return nil, errors.New(strings.TrimSpace(line))
	}
	if prefix != '$' {
		line, _ := reader.ReadString('\n')
		return nil, fmt.Errorf("unexpected RESP prefix %q", string(prefix)+line)
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 0 {
		return nil, errors.New("invalid RESP bulk length")
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	var crlf [2]byte
	if _, err := io.ReadFull(reader, crlf[:]); err != nil || crlf != [2]byte{'\r', '\n'} {
		return nil, errors.New("invalid RESP bulk terminator")
	}
	return payload, nil
}

func queryFailoverPeer(addr string, timeout time.Duration) (failoverPeerState, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return failoverPeerState{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if err := writeReplicationRESPCommand(conn, "SNUG.FAILOVER", "STATE"); err != nil {
		return failoverPeerState{}, err
	}
	payload, err := readRESPBulk(bufio.NewReader(conn))
	if err != nil {
		return failoverPeerState{}, err
	}
	var state failoverPeerState
	if err := json.Unmarshal(payload, &state); err != nil {
		return failoverPeerState{}, err
	}
	if state.NodeID == "" || (state.Role != "master" && state.Role != "replica") {
		return failoverPeerState{}, errors.New("invalid failover peer state")
	}
	return state, nil
}


func (s *Server) evaluatePeerFailover(now time.Time) (failoverElectionResult, error) {
	local := s.localFailoverState(now)
	if local.Role != "replica" || !local.MasterDown || local.MasterRunID == "" {
		return failoverElectionResult{}, nil
	}

	observations := []failoverObservation{{
		NodeID:     local.NodeID,
		MasterDown: local.MasterDown,
		Eligible:   local.Priority > 0,
		Offset:     local.Offset,
		Priority:   local.Priority,
	}}

	for _, addr := range s.failoverPeers {
		peer, err := queryFailoverPeer(addr, 200*time.Millisecond)
		if err != nil {
			continue
		}
		if peer.Role != "replica" || peer.MasterRunID == "" || peer.MasterRunID != local.MasterRunID {
			continue
		}
		observations = append(observations, failoverObservation{
			NodeID:     peer.NodeID,
			MasterDown: peer.MasterDown,
			Eligible:   peer.Priority > 0,
			Offset:     peer.Offset,
			Priority:   peer.Priority,
		})
	}
	return evaluateFailoverElection(observations, s.failoverQuorum), nil
}


func (s *Server) requestFailoverVote(now time.Time, lineage string, term uint64, candidateID string, candidateOffset int64, candidatePriority int) failoverVoteReply {
	local := s.localFailoverState(now)
	s.failoverVoteMu.Lock()
	defer s.failoverVoteMu.Unlock()

	if term < s.failoverTerm {
		return failoverVoteReply{Term: s.failoverTerm}
	}
	if term > s.failoverTerm {
		s.failoverTerm = term
		s.failoverVotedFor = ""
	}
	reply := failoverVoteReply{Term: s.failoverTerm}
	if local.Role != "replica" || !local.MasterDown || local.MasterRunID == "" || local.MasterRunID != lineage {
		return reply
	}
	if candidateID == "" || candidatePriority <= 0 {
		return reply
	}
	if candidateOffset < local.Offset {
		return reply
	}
	if s.failoverVotedFor != "" && s.failoverVotedFor != candidateID {
		return reply
	}
	s.failoverVotedFor = candidateID
	reply.Granted = true
	return reply
}

func queryFailoverVote(addr string, timeout time.Duration, lineage string, term uint64, candidateID string, offset int64, priority int) (failoverVoteReply, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return failoverVoteReply{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if err := writeReplicationRESPCommand(
		conn,
		"SNUG.FAILOVER", "REQUESTVOTE",
		lineage,
		strconv.FormatUint(term, 10),
		candidateID,
		strconv.FormatInt(offset, 10),
		strconv.Itoa(priority),
	); err != nil {
		return failoverVoteReply{}, err
	}
	payload, err := readRESPBulk(bufio.NewReader(conn))
	if err != nil {
		return failoverVoteReply{}, err
	}
	var reply failoverVoteReply
	if err := json.Unmarshal(payload, &reply); err != nil {
		return failoverVoteReply{}, err
	}
	return reply, nil
}
