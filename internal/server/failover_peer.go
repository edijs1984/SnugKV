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
	AdvertiseAddr string `json:"advertise_addr,omitempty"`
	GroupID     string `json:"group_id,omitempty"`
	ConfigEpoch uint64 `json:"config_epoch,omitempty"`
	JointActive bool   `json:"joint_active,omitempty"`
	PendingEpoch uint64 `json:"pending_epoch,omitempty"`
	Retired      bool   `json:"retired,omitempty"`
	RetiredAtEpoch uint64 `json:"retired_at_epoch,omitempty"`
	RetirePending bool `json:"retire_pending,omitempty"`
	RetirePendingEpoch uint64 `json:"retire_pending_epoch,omitempty"`
	Members     []string `json:"members,omitempty"`
	Quorum      int      `json:"quorum,omitempty"`
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

type failoverLeaseReply struct {
	Term      uint64 `json:"term"`
	Granted   bool   `json:"granted"`
	ExpiresMS int64  `json:"expires_ms"`
}

type failoverMembershipSnapshot struct {
	GroupID       string
	ConfigEpoch   uint64
	Peers         []string
	Quorum        int
	JointActive   bool
	PendingEpoch  uint64
	PendingPeers  []string
	PendingQuorum int
	CommitPending bool
	CommitOldEpoch uint64
	CommitEpoch uint64
	CommitMembers []string
	CommitQuorum int
	CommitTargets []string
	Retired bool
	RetiredAtEpoch uint64
	RetirePending bool
	RetirePendingEpoch uint64
	CommitRetireTargets []string
}

func (s *Server) failoverMembershipSnapshot() failoverMembershipSnapshot {
	s.failoverMembershipMu.RLock()
	defer s.failoverMembershipMu.RUnlock()
	return failoverMembershipSnapshot{
		GroupID:       s.failoverGroupID,
		ConfigEpoch:   s.failoverConfigEpoch,
		Peers:         append([]string(nil), s.failoverPeers...),
		Quorum:        s.failoverQuorum,
		JointActive:   s.failoverJointActive,
		PendingEpoch:  s.failoverPendingEpoch,
		PendingPeers:  append([]string(nil), s.failoverPendingPeers...),
		PendingQuorum: s.failoverPendingQuorum,
		CommitPending: s.failoverCommitPending,
		CommitOldEpoch: s.failoverCommitOldEpoch,
		CommitEpoch: s.failoverCommitEpoch,
		CommitMembers: append([]string(nil), s.failoverCommitMembers...),
		CommitQuorum: s.failoverCommitQuorum,
		CommitTargets: append([]string(nil), s.failoverCommitTargets...),
		Retired: s.failoverRetired,
		RetiredAtEpoch: s.failoverRetiredAtEpoch,
		RetirePending: s.failoverRetirePending,
		RetirePendingEpoch: s.failoverRetirePendingEpoch,
		CommitRetireTargets: append([]string(nil), s.failoverCommitRetireTargets...),
	}
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
	membership := s.failoverMembershipSnapshot()
	members := make([]string, 0, len(membership.Peers)+1)
	if s.failoverAdvertiseAddr != "" {
		members = append(members, s.failoverAdvertiseAddr)
	}
	members = append(members, membership.Peers...)
	s.failoverVoteMu.Lock()
	term := s.failoverTerm
	s.failoverVoteMu.Unlock()
	return failoverPeerState{
		NodeID:      nodeID,
		AdvertiseAddr: s.failoverAdvertiseAddr,
		GroupID:     membership.GroupID,
		ConfigEpoch: membership.ConfigEpoch,
		JointActive: membership.JointActive,
		PendingEpoch: membership.PendingEpoch,
		Retired: membership.Retired,
		RetiredAtEpoch: membership.RetiredAtEpoch,
		RetirePending: membership.RetirePending,
		RetirePendingEpoch: membership.RetirePendingEpoch,
		Members:     members,
		Quorum:      membership.Quorum,
		Role:        roleName,
		MasterDown:  masterDown,
		Offset:      offset,
		Priority:    s.failoverPriority,
		MasterRunID: masterRunID,
		Term:        term,
	}
}


func (s *Server) failoverMembershipMatches(groupID string, epoch uint64) bool {
	membership := s.failoverMembershipSnapshot()
	return membership.GroupID == groupID && membership.ConfigEpoch == epoch
}

func (s *Server) failoverPeerMembershipMatches(peer failoverPeerState) bool {
	return s.failoverMembershipMatches(peer.GroupID, peer.ConfigEpoch)
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

func queryFailoverPeer(addr string, timeout time.Duration, username, password string, controlAuth ...string) (failoverPeerState, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return failoverPeerState{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	reader := bufio.NewReader(conn)
	if err := authenticateInternalControlUpstream(conn, reader, username, password, internalControlSecret(controlAuth)); err != nil {
		return failoverPeerState{}, err
	}
	if err := writeReplicationRESPCommand(conn, "SNUG.FAILOVER", "STATE"); err != nil {
		return failoverPeerState{}, err
	}
	payload, err := readRESPBulk(reader)
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
	membership := s.failoverMembershipSnapshot()
	local := s.localFailoverState(now)
	if local.Role != "replica" || !local.MasterDown || local.MasterRunID == "" {
		return failoverElectionResult{}, nil
	}

	observations := []failoverObservation{{
		NodeID:     local.NodeID,
		MasterDown: local.MasterDown,
		Eligible:   local.Priority > 0 && !local.Retired,
		Offset:     local.Offset,
		Priority:   local.Priority,
	}}

	for _, addr := range membership.Peers {
		peer, err := queryFailoverPeer(addr, 200*time.Millisecond, s.replicationMasterUser, s.replicationMasterAuth, s.clusterControlAuth)
		if err != nil {
			continue
		}
		if !s.failoverPeerMembershipMatches(peer) || peer.Retired || peer.Role != "replica" || peer.MasterRunID == "" || peer.MasterRunID != local.MasterRunID {
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
	return evaluateFailoverElection(observations, membership.Quorum), nil
}


func (s *Server) requestFailoverVote(now time.Time, lineage string, term uint64, candidateID string, candidateOffset int64, candidatePriority int) (failoverVoteReply, error) {
	local := s.localFailoverState(now)
	s.failoverVoteMu.Lock()

	if term < s.failoverTerm {
		reply := failoverVoteReply{Term: s.failoverTerm}
		s.failoverVoteMu.Unlock()
		return reply, nil
	}
	changed := false
	if term > s.failoverTerm {
		s.failoverTerm = term
		s.failoverVotedFor = ""
		changed = true
	}
	reply := failoverVoteReply{Term: s.failoverTerm}
	eligible := !local.Retired &&
		local.Role == "replica" &&
		local.MasterDown &&
		local.MasterRunID != "" &&
		local.MasterRunID == lineage &&
		candidateID != "" &&
		candidatePriority > 0 &&
		candidateOffset >= local.Offset &&
		(s.failoverVotedFor == "" || s.failoverVotedFor == candidateID)
	if eligible && s.failoverVotedFor != candidateID {
		s.failoverVotedFor = candidateID
		changed = true
	}
	persistTerm := s.failoverTerm
	persistVote := s.failoverVotedFor
	s.failoverVoteMu.Unlock()

	if changed {
		if err := s.persistFailoverVoteState(persistTerm, persistVote); err != nil {
			return failoverVoteReply{Term: persistTerm}, err
		}
	}
	if eligible {
		reply.Granted = true
	}
	return reply, nil
}

func queryFailoverVote(addr string, timeout time.Duration, username, password, lineage string, term uint64, candidateID string, offset int64, priority int, controlAuth ...string) (failoverVoteReply, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return failoverVoteReply{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	reader := bufio.NewReader(conn)
	if err := authenticateInternalControlUpstream(conn, reader, username, password, internalControlSecret(controlAuth)); err != nil {
		return failoverVoteReply{}, err
	}
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
	payload, err := readRESPBulk(reader)
	if err != nil {
		return failoverVoteReply{}, err
	}
	var reply failoverVoteReply
	if err := json.Unmarshal(payload, &reply); err != nil {
		return failoverVoteReply{}, err
	}
	return reply, nil
}


type failoverRoundResult struct {
	Term          uint64
	CandidateID   string
	Votes         int
	QuorumReached bool
	Won           bool
}

type observedFailoverPeer struct {
	addr  string
	state failoverPeerState
}

func (s *Server) collectFailoverPeers(now time.Time) (failoverPeerState, []observedFailoverPeer) {
	membership := s.failoverMembershipSnapshot()
	local := s.localFailoverState(now)
	peers := make([]observedFailoverPeer, 0, len(membership.Peers))
	for _, addr := range membership.Peers {
		peer, err := queryFailoverPeer(addr, 200*time.Millisecond, s.replicationMasterUser, s.replicationMasterAuth, s.clusterControlAuth)
		if err != nil {
			continue
		}
		if !s.failoverPeerMembershipMatches(peer) ||
			peer.Retired ||
			peer.Role != "replica" ||
			peer.MasterRunID == "" ||
			peer.MasterRunID != local.MasterRunID {
			continue
		}
		peers = append(peers, observedFailoverPeer{addr: addr, state: peer})
	}
	return local, peers
}

func (s *Server) runFailoverElectionRound(now time.Time) (failoverRoundResult, error) {
	membership := s.failoverMembershipSnapshot()
	local, peers := s.collectFailoverPeers(now)
	if local.Role != "replica" || !local.MasterDown || local.MasterRunID == "" {
		return failoverRoundResult{}, nil
	}

	observations := make([]failoverObservation, 0, len(peers)+1)
	observations = append(observations, failoverObservation{
		NodeID:     local.NodeID,
		MasterDown: local.MasterDown,
		Eligible:   local.Priority > 0,
		Offset:     local.Offset,
		Priority:   local.Priority,
	})
	maxTerm := local.Term
	for _, peer := range peers {
		observations = append(observations, failoverObservation{
			NodeID:     peer.state.NodeID,
			MasterDown: peer.state.MasterDown,
			Eligible:   peer.state.Priority > 0,
			Offset:     peer.state.Offset,
			Priority:   peer.state.Priority,
		})
		if peer.state.Term > maxTerm {
			maxTerm = peer.state.Term
		}
	}

	election := evaluateFailoverElection(observations, membership.Quorum)
	result := failoverRoundResult{
		CandidateID:   election.CandidateID,
		QuorumReached: election.QuorumReached,
	}
	if !election.QuorumReached || election.CandidateID == "" || election.CandidateID != local.NodeID {
		return result, nil
	}

	term := maxTerm + 1
	result.Term = term

	selfVote, err := s.requestFailoverVote(
		now,
		local.MasterRunID,
		term,
		local.NodeID,
		local.Offset,
		local.Priority,
	)
	if err != nil {
		return result, err
	}
	if selfVote.Term > term {
		result.Term = selfVote.Term
		return result, nil
	}
	if selfVote.Granted {
		result.Votes++
	}

	for _, peer := range peers {
		vote, err := queryFailoverVote(
			peer.addr,
			200*time.Millisecond,
			s.replicationMasterUser,
			s.replicationMasterAuth,
			local.MasterRunID,
			term,
			local.NodeID,
			local.Offset,
			local.Priority,
			s.clusterControlAuth,
		)
		if err != nil {
			continue
		}
		if vote.Term > term {
			result.Term = vote.Term
			return result, nil
		}
		if vote.Granted {
			result.Votes++
		}
		if result.Votes >= membership.Quorum {
			result.Won = true
			return result, nil
		}
	}
	return result, nil
}


func (s *Server) requestFailoverLease(now time.Time, lineage string, term uint64, leaderID string, ttl time.Duration) failoverLeaseReply {
	local := s.localFailoverState(now)
	if local.Retired {
		return failoverLeaseReply{}
	}

	s.failoverVoteMu.Lock()
	currentTerm := s.failoverTerm
	s.failoverVoteMu.Unlock()

	reply := failoverLeaseReply{Term: currentTerm}
	if term < currentTerm || ttl <= 0 || ttl > 30*time.Second {
		return reply
	}
	localLineage := local.MasterRunID
	if localLineage == "" && local.Role == "master" {
		// The original primary uses its own replication run ID as the shard
		// lineage. Promoted leaders retain the failed primary's lineage.
		localLineage = local.NodeID
		s.failoverLeaderMu.RLock()
		if s.failoverLeaderActive && s.failoverLeaderLineage != "" {
			localLineage = s.failoverLeaderLineage
		}
		s.failoverLeaderMu.RUnlock()
	}
	if leaderID == "" {
		return reply
	}

	s.failoverLeaseMu.Lock()
	defer s.failoverLeaseMu.Unlock()

	lineageMatches := localLineage != "" && localLineage == lineage
	reparentedContinuation := local.Role == "replica" &&
		local.MasterRunID == leaderID &&
		s.failoverLeaseTerm == term &&
		s.failoverLeaseHolder == leaderID
	if !lineageMatches && !reparentedContinuation {
		return reply
	}

	if term < s.failoverLeaseTerm {
		reply.Term = s.failoverLeaseTerm
		return reply
	}
	if term > s.failoverLeaseTerm {
		s.failoverLeaseTerm = term
		s.failoverLeaseHolder = ""
		s.failoverLeaseUntil = time.Time{}
	}
	if !s.failoverLeaseUntil.IsZero() &&
		now.Before(s.failoverLeaseUntil) &&
		s.failoverLeaseHolder != "" &&
		s.failoverLeaseHolder != leaderID {
		reply.Term = s.failoverLeaseTerm
		reply.ExpiresMS = s.failoverLeaseUntil.UnixMilli()
		return reply
	}

	s.failoverLeaseHolder = leaderID
	s.failoverLeaseUntil = now.Add(ttl)
	reply.Term = s.failoverLeaseTerm
	reply.Granted = true
	reply.ExpiresMS = s.failoverLeaseUntil.UnixMilli()
	return reply
}

func queryFailoverLease(addr string, timeout time.Duration, username, password, lineage string, term uint64, leaderID string, ttl time.Duration, controlAuth ...string) (failoverLeaseReply, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return failoverLeaseReply{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	reader := bufio.NewReader(conn)
	if err := authenticateInternalControlUpstream(conn, reader, username, password, internalControlSecret(controlAuth)); err != nil {
		return failoverLeaseReply{}, err
	}
	if err := writeReplicationRESPCommand(
		conn,
		"SNUG.FAILOVER", "LEASE",
		lineage,
		strconv.FormatUint(term, 10),
		leaderID,
		strconv.FormatInt(ttl.Milliseconds(), 10),
	); err != nil {
		return failoverLeaseReply{}, err
	}
	payload, err := readRESPBulk(reader)
	if err != nil {
		return failoverLeaseReply{}, err
	}
	var reply failoverLeaseReply
	if err := json.Unmarshal(payload, &reply); err != nil {
		return failoverLeaseReply{}, err
	}
	return reply, nil
}


type failoverLeaseRoundResult struct {
	Term          uint64
	LeaderID      string
	Leases        int
	QuorumReached bool
	ExpiresAt     time.Time
}

func (s *Server) acquireFailoverLeaseRound(now time.Time, lineage string, term uint64, leaderID string) (failoverLeaseRoundResult, error) {
	membership := s.failoverMembershipSnapshot()
	result := failoverLeaseRoundResult{
		Term:     term,
		LeaderID: leaderID,
	}
	if term == 0 || leaderID == "" || lineage == "" || membership.Quorum <= 0 {
		return result, nil
	}

	const leaseTTL = 3 * time.Second

	localRequestStart := now
	local := s.requestFailoverLease(localRequestStart, lineage, term, leaderID, leaseTTL)
	if local.Term > term {
		result.Term = local.Term
		return result, nil
	}
	if local.Granted {
		result.Leases++
		result.ExpiresAt = localRequestStart.Add(leaseTTL - 250*time.Millisecond)
	}

	for _, addr := range membership.Peers {
		peer, err := queryFailoverPeer(addr, 200*time.Millisecond, s.replicationMasterUser, s.replicationMasterAuth, s.clusterControlAuth)
		if err != nil {
			continue
		}
		if !s.failoverPeerMembershipMatches(peer) ||
			peer.Role != "replica" ||
			(peer.MasterRunID != lineage && peer.MasterRunID != leaderID) {
			continue
		}
		requestStart := now
		reply, err := queryFailoverLease(addr, 200*time.Millisecond, s.replicationMasterUser, s.replicationMasterAuth, lineage, term, leaderID, leaseTTL, s.clusterControlAuth)
		if err != nil {
			continue
		}
		if reply.Term > term {
			result.Term = reply.Term
			return result, nil
		}
		if reply.Granted {
			result.Leases++
			expires := requestStart.Add(leaseTTL - 250*time.Millisecond)
			if result.ExpiresAt.IsZero() || expires.Before(result.ExpiresAt) {
				result.ExpiresAt = expires
			}
		}
		if result.Leases >= membership.Quorum {
			result.QuorumReached = true
			return result, nil
		}
	}
	return result, nil
}


func (s *Server) activateFailoverLeader(term uint64, lineage string, leaderID string, expiresAt time.Time) {
	s.failoverLeaderMu.Lock()
	s.failoverLeaderActive = true
	s.failoverLeaderTerm = term
	s.failoverLeaderLineage = lineage
	s.failoverLeaderID = leaderID
	s.failoverLeaderLeaseUntil = expiresAt
	s.failoverLeaderFenced = expiresAt.IsZero() || !time.Now().Before(expiresAt)
	s.failoverLeaderMu.Unlock()
}

func (s *Server) failoverLeaderState() (active bool, term uint64, lineage, leaderID string, expiresAt time.Time, fenced bool) {
	s.failoverLeaderMu.RLock()
	defer s.failoverLeaderMu.RUnlock()
	return s.failoverLeaderActive,
		s.failoverLeaderTerm,
		s.failoverLeaderLineage,
		s.failoverLeaderID,
		s.failoverLeaderLeaseUntil,
		s.failoverLeaderFenced
}

func (s *Server) updateFailoverLeaderLease(expiresAt time.Time, fenced bool) {
	s.failoverLeaderMu.Lock()
	if s.failoverLeaderActive {
		s.failoverLeaderLeaseUntil = expiresAt
		s.failoverLeaderFenced = fenced
	}
	s.failoverLeaderMu.Unlock()
}

func (s *Server) failoverWriteFenceStatus(now time.Time) (fenced bool, reason, leaseHolder string, leaseUntil time.Time) {
	s.failoverLeaderMu.RLock()
	leaderActive := s.failoverLeaderActive
	leaderFenced := s.failoverLeaderFenced
	leaderLeaseUntil := s.failoverLeaderLeaseUntil
	leaderID := s.failoverLeaderID
	s.failoverLeaderMu.RUnlock()

	if leaderActive && (leaderFenced ||
		leaderLeaseUntil.IsZero() ||
		!now.Before(leaderLeaseUntil)) {
		return true, "leader_lease_invalid", leaderID, leaderLeaseUntil
	}

	s.failoverLeaseMu.Lock()
	leaseHolder = s.failoverLeaseHolder
	leaseUntil = s.failoverLeaseUntil
	s.failoverLeaseMu.Unlock()

	if leaseHolder == "" || leaseUntil.IsZero() || !now.Before(leaseUntil) {
		return false, "", leaseHolder, leaseUntil
	}

	s.replication.mu.RLock()
	localID := s.replication.runID
	s.replication.mu.RUnlock()

	if localID != "" && leaseHolder != localID {
		return true, "foreign_leader_lease", leaseHolder, leaseUntil
	}
	return false, "", leaseHolder, leaseUntil
}

func (s *Server) failoverWritesFenced(now time.Time) bool {
	fenced, _, _, _ := s.failoverWriteFenceStatus(now)
	return fenced
}

func (s *Server) deactivateFailoverLeader() {
	s.failoverLeaderMu.Lock()
	s.failoverLeaderActive = false
	s.failoverLeaderTerm = 0
	s.failoverLeaderLineage = ""
	s.failoverLeaderID = ""
	s.failoverLeaderLeaseUntil = time.Time{}
	s.failoverLeaderFenced = false
	s.failoverLeaderMu.Unlock()
}

func (s *Server) establishPrimaryQuorumLease(now time.Time) error {
	membership := s.failoverMembershipSnapshot()
	if membership.Retired || membership.JointActive || membership.Quorum <= 0 || len(membership.Peers) == 0 {
		return nil
	}

	local := s.localFailoverState(now)
	if local.Role != "master" || local.NodeID == "" {
		return nil
	}

	s.failoverVoteMu.Lock()
	voteTerm := s.failoverTerm
	s.failoverVoteMu.Unlock()
	s.failoverLeaseMu.Lock()
	leaseTerm := s.failoverLeaseTerm
	s.failoverLeaseMu.Unlock()

	term := voteTerm
	if leaseTerm > term {
		term = leaseTerm
	}
	term++

	lease, err := s.acquireFailoverLeaseRound(now, local.NodeID, term, local.NodeID)
	if err != nil {
		return err
	}
	if !lease.QuorumReached || lease.ExpiresAt.IsZero() {
		// Activate a fenced leader state so the primary cannot continue serving
		// writes without quorum.
		s.activateFailoverLeader(term, local.NodeID, local.NodeID, time.Time{})
		return nil
	}
	s.activateFailoverLeader(term, local.NodeID, local.NodeID, lease.ExpiresAt)
	return nil
}

func (s *Server) maintainFailoverLeaderLease(now time.Time) error {
	active, term, lineage, leaderID, expiresAt, _ := s.failoverLeaderState()
	if !active {
		return nil
	}

	// Renew only in the final third of the current lease to avoid turning every
	// cleanup tick into a peer RPC burst.
	if !expiresAt.IsZero() && now.Before(expiresAt.Add(-time.Second)) {
		return nil
	}

	lease, err := s.acquireFailoverLeaseRound(now, lineage, term, leaderID)
	if err != nil {
		if expiresAt.IsZero() || !now.Before(expiresAt) {
			s.updateFailoverLeaderLease(expiresAt, true)
		}
		return err
	}
	if lease.QuorumReached && !lease.ExpiresAt.IsZero() {
		s.updateFailoverLeaderLease(lease.ExpiresAt, false)
		if err := s.convergeClusterFailoverOwnership(); err != nil {
			return err
		}
		s.convergeFailoverReplicas(now)
		return nil
	}
	if expiresAt.IsZero() || !now.Before(expiresAt) {
		s.updateFailoverLeaderLease(expiresAt, true)
	}
	return nil
}


type failoverReparentReply struct {
	Term     uint64 `json:"term"`
	Accepted bool   `json:"accepted"`
}

func (s *Server) resolveFailoverLeaderAddr(leaderID string) (string, error) {
	membership := s.failoverMembershipSnapshot()
	if leaderID == "" {
		return "", errors.New("empty failover leader id")
	}
	for _, addr := range membership.Peers {
		state, err := queryFailoverPeer(
			addr,
			200*time.Millisecond,
			s.replicationMasterUser,
			s.replicationMasterAuth,
			s.clusterControlAuth,
		)
		if err != nil {
			continue
		}
		if s.failoverPeerMembershipMatches(state) && !state.Retired && state.NodeID == leaderID && state.Role == "master" {
			return addr, nil
		}
	}
	return "", errors.New("failover leader endpoint not found")
}

func (s *Server) requestFailoverReparent(now time.Time, lineage string, term uint64, leaderID string) (failoverReparentReply, error) {
	if s.failoverMembershipSnapshot().Retired {
		return failoverReparentReply{}, nil
	}
	s.failoverVoteMu.Lock()
	currentTerm := s.failoverTerm
	s.failoverVoteMu.Unlock()

	reply := failoverReparentReply{Term: currentTerm}
	if term < currentTerm || lineage == "" || leaderID == "" {
		return reply, nil
	}

	s.replication.mu.RLock()
	role := s.replication.role
	currentLineage := s.replication.masterRunID
	s.replication.mu.RUnlock()
	if role != replicationReplica || currentLineage == "" || currentLineage != lineage {
		return reply, nil
	}

	s.failoverLeaseMu.Lock()
	leaseTerm := s.failoverLeaseTerm
	leaseHolder := s.failoverLeaseHolder
	leaseUntil := s.failoverLeaseUntil
	s.failoverLeaseMu.Unlock()
	if leaseTerm != term ||
		leaseHolder != leaderID ||
		leaseUntil.IsZero() ||
		!now.Before(leaseUntil) {
		return reply, nil
	}

	addr, err := s.resolveFailoverLeaderAddr(leaderID)
	if err != nil {
		return reply, nil
	}
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return reply, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return reply, errors.New("invalid failover leader port")
	}

	if err := s.persistReplicationCheckpointClearLocked(host, port); err != nil {
		s.durabilityFailed = true
		return reply, errors.New("failover reparent persistence update failed")
	}
	if err := s.clearReplicationPersistence(); err != nil {
		return reply, errors.New("failover reparent persistence update failed")
	}
	if err := s.repairClusterFailoverMemberOwnership(addr); err != nil {
		return reply, fmt.Errorf("failover reparent cluster ownership update failed: %w", err)
	}
	s.startReplicaFollow(host, port)
	reply.Accepted = true
	return reply, nil
}

func queryFailoverReparent(addr string, timeout time.Duration, username, password, lineage string, term uint64, leaderID string, controlAuth ...string) (failoverReparentReply, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return failoverReparentReply{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	reader := bufio.NewReader(conn)
	if err := authenticateInternalControlUpstream(conn, reader, username, password, internalControlSecret(controlAuth)); err != nil {
		return failoverReparentReply{}, err
	}
	if err := writeReplicationRESPCommand(
		conn,
		"SNUG.FAILOVER", "REPARENT",
		lineage,
		strconv.FormatUint(term, 10),
		leaderID,
	); err != nil {
		return failoverReparentReply{}, err
	}
	payload, err := readRESPBulk(reader)
	if err != nil {
		return failoverReparentReply{}, err
	}
	var reply failoverReparentReply
	if err := json.Unmarshal(payload, &reply); err != nil {
		return failoverReparentReply{}, err
	}
	return reply, nil
}

func (s *Server) convergeFailoverReplicas(now time.Time) {
	membership := s.failoverMembershipSnapshot()
	active, term, lineage, leaderID, expiresAt, fenced := s.failoverLeaderState()
	if !active || fenced || lineage == "" || leaderID == "" || expiresAt.IsZero() || !now.Before(expiresAt) {
		return
	}
	for _, addr := range membership.Peers {
		state, err := queryFailoverPeer(
			addr,
			200*time.Millisecond,
			s.replicationMasterUser,
			s.replicationMasterAuth,
			s.clusterControlAuth,
		)
		if err != nil || !s.failoverPeerMembershipMatches(state) || state.Retired || state.NodeID == leaderID {
			continue
		}

		if state.Role == "master" {
			_, _ = queryFailoverDemote(
				addr,
				200*time.Millisecond,
				s.replicationMasterUser,
				s.replicationMasterAuth,
				lineage,
				term,
				leaderID,
				s.clusterControlAuth,
			)
			continue
		}

		if state.Role == "replica" && state.MasterRunID == lineage {
			_, _ = queryFailoverReparent(
				addr,
				200*time.Millisecond,
				s.replicationMasterUser,
				s.replicationMasterAuth,
				lineage,
				term,
				leaderID,
				s.clusterControlAuth,
			)
		}
	}
}


type failoverDemoteReply struct {
	Term     uint64 `json:"term"`
	Accepted bool   `json:"accepted"`
}

func (s *Server) requestFailoverDemote(now time.Time, lineage string, term uint64, leaderID string, expectedTargetAddr ...string) (failoverDemoteReply, error) {
	if s.failoverMembershipSnapshot().Retired {
		return failoverDemoteReply{}, nil
	}
	s.failoverVoteMu.Lock()
	currentTerm := s.failoverTerm
	s.failoverVoteMu.Unlock()

	reply := failoverDemoteReply{Term: currentTerm}
	if term < currentTerm || lineage == "" || leaderID == "" {
		return reply, nil
	}

	local := s.localFailoverState(now)
	if local.Role != "master" {
		return reply, nil
	}

	// A returning old primary may have restarted and therefore have a new
	// replication run ID. Network demotion requests carry the exact membership
	// address the leader intended to demote; direct in-process callers retain
	// the historical lineage identity check.
	targetAddr := ""
	if len(expectedTargetAddr) > 0 {
		targetAddr = expectedTargetAddr[0]
	}
	if targetAddr != "" {
		if s.failoverAdvertiseAddr == "" || s.failoverAdvertiseAddr != targetAddr {
			return reply, nil
		}
	} else if local.NodeID != lineage {
		return reply, nil
	}

	verified, higherTerm := s.verifyFailoverLeaderQuorum(now, lineage, term, leaderID)
	if higherTerm > term {
		reply.Term = higherTerm
		return reply, nil
	}
	if !verified {
		return reply, nil
	}

	addr, err := s.resolveFailoverLeaderAddr(leaderID)
	if err != nil {
		return reply, nil
	}
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return reply, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return reply, errors.New("invalid failover leader port")
	}

	s.failoverLeaderMu.RLock()
	activeLocalLeader := s.failoverLeaderActive
	localLeaderLineage := s.failoverLeaderLineage
	localLeaderLeaseUntil := s.failoverLeaderLeaseUntil
	localLeaderFenced := s.failoverLeaderFenced
	s.failoverLeaderMu.RUnlock()
	if activeLocalLeader && localLeaderLineage != "" && localLeaderLineage != lineage {
		// A restarted old primary can create a fenced self-leader record using
		// its new run ID. Permit recovery only when the address proof above was
		// supplied and that local leader state is already fenced or expired.
		activeLease := !localLeaderFenced &&
			!localLeaderLeaseUntil.IsZero() &&
			now.Before(localLeaderLeaseUntil)
		if targetAddr == "" || activeLease {
			return reply, nil
		}
	}

	if err := s.persistReplicationCheckpointClearLocked(host, port); err != nil {
		s.durabilityFailed = true
		return reply, errors.New("failover demote persistence update failed")
	}
	if err := s.clearReplicationPersistence(); err != nil {
		return reply, errors.New("failover demote persistence update failed")
	}
	if err := s.repairClusterFailoverMemberOwnership(addr); err != nil {
		return reply, fmt.Errorf("failover demote cluster ownership update failed: %w", err)
	}
	s.startReplicaFollow(host, port)
	reply.Accepted = true
	return reply, nil
}

func queryFailoverDemote(addr string, timeout time.Duration, username, password, lineage string, term uint64, leaderID string, controlAuth ...string) (failoverDemoteReply, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return failoverDemoteReply{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	reader := bufio.NewReader(conn)
	if err := authenticateInternalControlUpstream(conn, reader, username, password, internalControlSecret(controlAuth)); err != nil {
		return failoverDemoteReply{}, err
	}
	if err := writeReplicationRESPCommand(
		conn,
		"SNUG.FAILOVER", "DEMOTE",
		lineage,
		strconv.FormatUint(term, 10),
		leaderID,
		addr,
	); err != nil {
		return failoverDemoteReply{}, err
	}
	payload, err := readRESPBulk(reader)
	if err != nil {
		return failoverDemoteReply{}, err
	}
	var reply failoverDemoteReply
	if err := json.Unmarshal(payload, &reply); err != nil {
		return failoverDemoteReply{}, err
	}
	return reply, nil
}


func (s *Server) verifyFailoverLeaderQuorum(now time.Time, lineage string, term uint64, leaderID string) (bool, uint64) {
	membership := s.failoverMembershipSnapshot()
	if lineage == "" || leaderID == "" || term == 0 || membership.Quorum <= 0 {
		return false, term
	}

	const leaseTTL = 3 * time.Second
	grants := 0
	highestTerm := term

	for _, addr := range membership.Peers {
		state, err := queryFailoverPeer(
			addr,
			200*time.Millisecond,
			s.replicationMasterUser,
			s.replicationMasterAuth,
			s.clusterControlAuth,
		)
		if err != nil {
			continue
		}

		// The elected leader no longer reports masterRunID after promotion, so
		// identify it by node ID. Other voters must still report the original
		// lineage.
		if !s.failoverPeerMembershipMatches(state) {
			continue
		}
		if state.NodeID != leaderID &&
			(state.Role != "replica" || state.MasterRunID != lineage) {
			continue
		}

		reply, err := queryFailoverLease(
			addr,
			200*time.Millisecond,
			s.replicationMasterUser,
			s.replicationMasterAuth,
			lineage,
			term,
			leaderID,
			leaseTTL,
			s.clusterControlAuth,
		)
		if err != nil {
			continue
		}
		if reply.Term > highestTerm {
			highestTerm = reply.Term
		}
		if reply.Term > term {
			return false, highestTerm
		}
		if reply.Granted {
			grants++
		}
		if grants >= membership.Quorum {
			return true, highestTerm
		}
	}

	return false, highestTerm
}
