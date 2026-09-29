package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"
)

type failoverMembershipReply struct {
	GroupID     string `json:"group_id"`
	ConfigEpoch uint64 `json:"config_epoch"`
	PendingEpoch uint64 `json:"pending_epoch,omitempty"`
	Accepted    bool   `json:"accepted"`
	Joint       bool   `json:"joint"`
}

func validateFailoverPeerSet(peers []string, quorum int) error {
	seen := make(map[string]struct{}, len(peers))
	for _, peer := range peers {
		host, portText, err := net.SplitHostPort(peer)
		if err != nil || host == "" || portText == "" {
			return errors.New("invalid failover peer address")
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port <= 0 || port > 65535 {
			return errors.New("invalid failover peer port")
		}
		if _, exists := seen[peer]; exists {
			return errors.New("duplicate failover peer address")
		}
		seen[peer] = struct{}{}
	}
	totalNodes := len(peers) + 1
	majority := totalNodes/2 + 1
	if quorum < majority || quorum > totalNodes {
		return errors.New("failover quorum is not a majority of the proposed membership")
	}
	return nil
}

func parseFailoverPeerCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	peers := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			peers = append(peers, part)
		}
	}
	return peers
}

func (s *Server) prepareFailoverMembership(groupID string, currentEpoch, newEpoch uint64, peers []string, quorum int) (failoverMembershipReply, error) {
	if groupID == "" {
		return failoverMembershipReply{}, errors.New("failover membership requires a group id")
	}
	if newEpoch <= currentEpoch {
		return failoverMembershipReply{}, errors.New("new failover membership epoch must increase")
	}
	if err := validateFailoverPeerSet(peers, quorum); err != nil {
		return failoverMembershipReply{}, err
	}
	if _, ok := replicationPersistencePaths.Load(s); !ok {
		return failoverMembershipReply{}, errors.New("failover membership persistence is unavailable")
	}

	s.failoverMembershipMu.Lock()
	if s.failoverGroupID != groupID || s.failoverConfigEpoch != currentEpoch {
		reply := failoverMembershipReply{
			GroupID: s.failoverGroupID,
			ConfigEpoch: s.failoverConfigEpoch,
			PendingEpoch: s.failoverPendingEpoch,
			Joint: s.failoverJointActive,
		}
		s.failoverMembershipMu.Unlock()
		return reply, nil
	}
	if s.failoverJointActive {
		if s.failoverPendingEpoch == newEpoch &&
			s.failoverPendingQuorum == quorum &&
			stringSlicesEqual(s.failoverPendingPeers, peers) {
			reply := failoverMembershipReply{
				GroupID: groupID,
				ConfigEpoch: currentEpoch,
				PendingEpoch: newEpoch,
				Accepted: true,
				Joint: true,
			}
			s.failoverMembershipMu.Unlock()
			return reply, nil
		}
		s.failoverMembershipMu.Unlock()
		return failoverMembershipReply{}, errors.New("another failover membership transition is active")
	}
	oldEpoch := s.failoverPendingEpoch
	oldPeers := append([]string(nil), s.failoverPendingPeers...)
	oldQuorum := s.failoverPendingQuorum
	oldJoint := s.failoverJointActive

	s.failoverJointActive = true
	s.failoverPendingEpoch = newEpoch
	s.failoverPendingPeers = append([]string(nil), peers...)
	s.failoverPendingQuorum = quorum
	s.failoverMembershipMu.Unlock()

	if err := s.persistFailoverMembershipState(); err != nil {
		s.failoverMembershipMu.Lock()
		s.failoverJointActive = oldJoint
		s.failoverPendingEpoch = oldEpoch
		s.failoverPendingPeers = oldPeers
		s.failoverPendingQuorum = oldQuorum
		s.failoverMembershipMu.Unlock()
		return failoverMembershipReply{}, err
	}

	return failoverMembershipReply{
		GroupID: groupID,
		ConfigEpoch: currentEpoch,
		PendingEpoch: newEpoch,
		Accepted: true,
		Joint: true,
	}, nil
}

func (s *Server) commitFailoverMembership(groupID string, newEpoch uint64) (failoverMembershipReply, error) {
	if _, ok := replicationPersistencePaths.Load(s); !ok {
		return failoverMembershipReply{}, errors.New("failover membership persistence is unavailable")
	}

	s.failoverMembershipMu.Lock()
	if s.failoverGroupID != groupID || !s.failoverJointActive || s.failoverPendingEpoch != newEpoch {
		reply := failoverMembershipReply{
			GroupID: s.failoverGroupID,
			ConfigEpoch: s.failoverConfigEpoch,
			PendingEpoch: s.failoverPendingEpoch,
			Joint: s.failoverJointActive,
		}
		s.failoverMembershipMu.Unlock()
		return reply, nil
	}

	oldEpoch := s.failoverConfigEpoch
	oldPeers := append([]string(nil), s.failoverPeers...)
	oldQuorum := s.failoverQuorum
	oldPendingEpoch := s.failoverPendingEpoch
	oldPendingPeers := append([]string(nil), s.failoverPendingPeers...)
	oldPendingQuorum := s.failoverPendingQuorum
	oldRetirePending := s.failoverRetirePending
	oldRetirePendingEpoch := s.failoverRetirePendingEpoch

	s.failoverConfigEpoch = s.failoverPendingEpoch
	s.failoverPeers = append([]string(nil), s.failoverPendingPeers...)
	s.failoverQuorum = s.failoverPendingQuorum
	s.failoverJointActive = false
	s.failoverPendingEpoch = 0
	s.failoverPendingPeers = nil
	s.failoverPendingQuorum = 0
	if s.failoverRetirePending && s.failoverRetirePendingEpoch == newEpoch {
		s.failoverRetirePending = false
		s.failoverRetirePendingEpoch = 0
	}
	s.failoverMembershipMu.Unlock()

	if err := s.persistFailoverMembershipState(); err != nil {
		s.failoverMembershipMu.Lock()
		s.failoverConfigEpoch = oldEpoch
		s.failoverPeers = oldPeers
		s.failoverQuorum = oldQuorum
		s.failoverJointActive = true
		s.failoverPendingEpoch = oldPendingEpoch
		s.failoverPendingPeers = oldPendingPeers
		s.failoverPendingQuorum = oldPendingQuorum
		s.failoverRetirePending = oldRetirePending
		s.failoverRetirePendingEpoch = oldRetirePendingEpoch
		s.failoverMembershipMu.Unlock()
		return failoverMembershipReply{}, err
	}

	return failoverMembershipReply{
		GroupID: groupID,
		ConfigEpoch: newEpoch,
		Accepted: true,
		Joint: false,
	}, nil
}

func (s *Server) abortFailoverMembership(groupID string, pendingEpoch uint64) (failoverMembershipReply, error) {
	if _, ok := replicationPersistencePaths.Load(s); !ok {
		return failoverMembershipReply{}, errors.New("failover membership persistence is unavailable")
	}

	s.failoverMembershipMu.Lock()
	if s.failoverGroupID != groupID || !s.failoverJointActive || s.failoverPendingEpoch != pendingEpoch {
		reply := failoverMembershipReply{
			GroupID: s.failoverGroupID,
			ConfigEpoch: s.failoverConfigEpoch,
			PendingEpoch: s.failoverPendingEpoch,
			Joint: s.failoverJointActive,
		}
		s.failoverMembershipMu.Unlock()
		return reply, nil
	}
	oldEpoch := s.failoverPendingEpoch
	oldPeers := append([]string(nil), s.failoverPendingPeers...)
	oldQuorum := s.failoverPendingQuorum
	s.failoverJointActive = false
	s.failoverPendingEpoch = 0
	s.failoverPendingPeers = nil
	s.failoverPendingQuorum = 0
	s.failoverMembershipMu.Unlock()

	if err := s.persistFailoverMembershipState(); err != nil {
		s.failoverMembershipMu.Lock()
		s.failoverJointActive = true
		s.failoverPendingEpoch = oldEpoch
		s.failoverPendingPeers = oldPeers
		s.failoverPendingQuorum = oldQuorum
		s.failoverMembershipMu.Unlock()
		return failoverMembershipReply{}, err
	}
	membership := s.failoverMembershipSnapshot()
	return failoverMembershipReply{
		GroupID: membership.GroupID,
		ConfigEpoch: membership.ConfigEpoch,
		Accepted: true,
		Joint: false,
	}, nil
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}


func deriveFailoverPeersForMember(members []string, self string) ([]string, error) {
	if self == "" {
		return nil, errors.New("failover_advertise_addr is required for dynamic membership")
	}
	seen := make(map[string]struct{}, len(members))
	foundSelf := false
	peers := make([]string, 0, len(members))
	for _, member := range members {
		member = strings.TrimSpace(member)
		if member == "" {
			return nil, errors.New("empty failover member address")
		}
		if _, exists := seen[member]; exists {
			return nil, errors.New("duplicate failover member address")
		}
		seen[member] = struct{}{}
		host, portText, err := net.SplitHostPort(member)
		if err != nil || host == "" || portText == "" {
			return nil, errors.New("invalid failover member address")
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port <= 0 || port > 65535 {
			return nil, errors.New("invalid failover member port")
		}
		if member == self {
			foundSelf = true
			continue
		}
		peers = append(peers, member)
	}
	if !foundSelf {
		return nil, errors.New("dynamic membership must include this node")
	}
	return peers, nil
}

func (s *Server) prepareFailoverMembershipMembers(groupID string, currentEpoch, newEpoch uint64, members []string, quorum int) (failoverMembershipReply, error) {
	peers, err := deriveFailoverPeersForMember(members, s.failoverAdvertiseAddr)
	if err != nil {
		return failoverMembershipReply{}, err
	}
	return s.prepareFailoverMembership(groupID, currentEpoch, newEpoch, peers, quorum)
}

func (s *Server) failoverCurrentMembers() ([]string, error) {
	membership := s.failoverMembershipSnapshot()
	if s.failoverAdvertiseAddr == "" {
		return nil, errors.New("failover_advertise_addr is required for dynamic membership")
	}
	members := make([]string, 0, len(membership.Peers)+1)
	members = append(members, s.failoverAdvertiseAddr)
	members = append(members, membership.Peers...)
	return members, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}


type failoverMembershipChangeResult struct {
	OldAcks       int
	NewAcks       int
	OldQuorum     int
	NewQuorum     int
	Prepared      bool
	Committed     bool
	PreparedPeers []string
}

func queryFailoverMembershipPrepare(addr string, timeout time.Duration, username, password, groupID string, currentEpoch, newEpoch uint64, members []string, quorum int, controlAuth ...string) (failoverMembershipReply, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return failoverMembershipReply{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	reader := bufio.NewReader(conn)
	if err := authenticateInternalControlUpstream(conn, reader, username, password, internalControlSecret(controlAuth)); err != nil {
		return failoverMembershipReply{}, err
	}
	if err := writeReplicationRESPCommand(
		conn,
		"SNUG.FAILOVER", "MEMBERSHIPPREPARE",
		groupID,
		strconv.FormatUint(currentEpoch, 10),
		strconv.FormatUint(newEpoch, 10),
		strconv.Itoa(quorum),
		strings.Join(members, ","),
	); err != nil {
		return failoverMembershipReply{}, err
	}
	payload, err := readRESPBulk(reader)
	if err != nil {
		return failoverMembershipReply{}, err
	}
	var reply failoverMembershipReply
	if err := json.Unmarshal(payload, &reply); err != nil {
		return failoverMembershipReply{}, err
	}
	return reply, nil
}

func queryFailoverMembershipCommit(addr string, timeout time.Duration, username, password, groupID string, epoch uint64, controlAuth ...string) (failoverMembershipReply, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return failoverMembershipReply{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	reader := bufio.NewReader(conn)
	if err := authenticateInternalControlUpstream(conn, reader, username, password, internalControlSecret(controlAuth)); err != nil {
		return failoverMembershipReply{}, err
	}
	if err := writeReplicationRESPCommand(
		conn,
		"SNUG.FAILOVER", "MEMBERSHIPCOMMIT",
		groupID,
		strconv.FormatUint(epoch, 10),
	); err != nil {
		return failoverMembershipReply{}, err
	}
	payload, err := readRESPBulk(reader)
	if err != nil {
		return failoverMembershipReply{}, err
	}
	var reply failoverMembershipReply
	if err := json.Unmarshal(payload, &reply); err != nil {
		return failoverMembershipReply{}, err
	}
	return reply, nil
}

func queryFailoverMembershipAbort(addr string, timeout time.Duration, username, password, groupID string, epoch uint64, controlAuth ...string) (failoverMembershipReply, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return failoverMembershipReply{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	reader := bufio.NewReader(conn)
	if err := authenticateInternalControlUpstream(conn, reader, username, password, internalControlSecret(controlAuth)); err != nil {
		return failoverMembershipReply{}, err
	}
	if err := writeReplicationRESPCommand(
		conn,
		"SNUG.FAILOVER", "MEMBERSHIPABORT",
		groupID,
		strconv.FormatUint(epoch, 10),
	); err != nil {
		return failoverMembershipReply{}, err
	}
	payload, err := readRESPBulk(reader)
	if err != nil {
		return failoverMembershipReply{}, err
	}
	var reply failoverMembershipReply
	if err := json.Unmarshal(payload, &reply); err != nil {
		return failoverMembershipReply{}, err
	}
	return reply, nil
}

func unionStrings(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, values := range [][]string{a, b} {
		for _, value := range values {
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	return out
}


func (s *Server) armFailoverMembershipCommitRecovery(oldEpoch, newEpoch uint64, members []string, quorum int, targets, retireTargets []string) error {
	s.failoverMembershipMu.Lock()
	oldPending := s.failoverCommitPending
	oldOldEpoch := s.failoverCommitOldEpoch
	oldCommitEpoch := s.failoverCommitEpoch
	oldMembers := append([]string(nil), s.failoverCommitMembers...)
	oldQuorum := s.failoverCommitQuorum
	oldTargets := append([]string(nil), s.failoverCommitTargets...)
	oldRetireTargets := append([]string(nil), s.failoverCommitRetireTargets...)
	s.failoverCommitPending = true
	s.failoverCommitOldEpoch = oldEpoch
	s.failoverCommitEpoch = newEpoch
	s.failoverCommitMembers = append([]string(nil), members...)
	s.failoverCommitQuorum = quorum
	s.failoverCommitTargets = append([]string(nil), targets...)
	s.failoverCommitRetireTargets = append([]string(nil), retireTargets...)
	s.failoverMembershipMu.Unlock()

	if err := s.persistFailoverMembershipState(); err != nil {
		s.failoverMembershipMu.Lock()
		s.failoverCommitPending = oldPending
		s.failoverCommitOldEpoch = oldOldEpoch
		s.failoverCommitEpoch = oldCommitEpoch
		s.failoverCommitMembers = oldMembers
		s.failoverCommitQuorum = oldQuorum
		s.failoverCommitTargets = oldTargets
		s.failoverCommitRetireTargets = oldRetireTargets
		s.failoverMembershipMu.Unlock()
		return err
	}
	return nil
}

func (s *Server) clearFailoverMembershipCommitRecovery() error {
	s.failoverMembershipMu.Lock()
	oldPending := s.failoverCommitPending
	oldOldEpoch := s.failoverCommitOldEpoch
	oldCommitEpoch := s.failoverCommitEpoch
	oldMembers := append([]string(nil), s.failoverCommitMembers...)
	oldQuorum := s.failoverCommitQuorum
	oldTargets := append([]string(nil), s.failoverCommitTargets...)
	oldRetireTargets := append([]string(nil), s.failoverCommitRetireTargets...)
	s.failoverCommitPending = false
	s.failoverCommitOldEpoch = 0
	s.failoverCommitEpoch = 0
	s.failoverCommitMembers = nil
	s.failoverCommitQuorum = 0
	s.failoverCommitTargets = nil
	s.failoverCommitRetireTargets = nil
	s.failoverMembershipMu.Unlock()

	if err := s.persistFailoverMembershipState(); err != nil {
		s.failoverMembershipMu.Lock()
		s.failoverCommitPending = oldPending
		s.failoverCommitOldEpoch = oldOldEpoch
		s.failoverCommitEpoch = oldCommitEpoch
		s.failoverCommitMembers = oldMembers
		s.failoverCommitQuorum = oldQuorum
		s.failoverCommitTargets = oldTargets
		s.failoverCommitRetireTargets = oldRetireTargets
		s.failoverMembershipMu.Unlock()
		return err
	}
	return nil
}

func (s *Server) retryFailoverMembershipCommit(now time.Time) error {
	membership := s.failoverMembershipSnapshot()
	if !membership.CommitPending {
		return nil
	}

	s.failoverMembershipMu.Lock()
	if !s.failoverCommitLastRetry.IsZero() && now.Sub(s.failoverCommitLastRetry) < time.Second {
		s.failoverMembershipMu.Unlock()
		return nil
	}
	s.failoverCommitLastRetry = now
	s.failoverMembershipMu.Unlock()

	// Removed members must be durably retired before the new membership is
	// allowed to commit. Recovery always finishes retirement first.
	for _, addr := range membership.CommitRetireTargets {
		state, err := queryFailoverPeer(
			addr,
			300*time.Millisecond,
			s.replicationMasterUser,
			s.replicationMasterAuth,
			s.clusterControlAuth,
		)
		if err != nil {
			return nil
		}
		if state.GroupID != membership.GroupID {
			return nil
		}
		if state.Retired && state.RetiredAtEpoch == membership.CommitEpoch {
			continue
		}
		if state.ConfigEpoch != membership.CommitOldEpoch {
			return nil
		}
		if !state.RetirePending || state.RetirePendingEpoch != membership.CommitEpoch {
			reply, prepErr := queryFailoverRetirePrepare(
				addr,
				300*time.Millisecond,
				s.replicationMasterUser,
				s.replicationMasterAuth,
				membership.GroupID,
				membership.CommitOldEpoch,
				membership.CommitEpoch,
			)
			if prepErr != nil || !reply.Accepted {
				return nil
			}
		}
		reply, retireErr := queryFailoverRetire(
			addr,
			300*time.Millisecond,
			s.replicationMasterUser,
			s.replicationMasterAuth,
			membership.GroupID,
			membership.CommitOldEpoch,
			membership.CommitEpoch,
			s.clusterControlAuth,
		)
		if retireErr != nil || !reply.Accepted || !reply.Retired {
			return nil
		}
	}

	// If the coordinator crashed after recording commit intent but before its
	// own local COMMIT, recover by committing forward only after every removed
	// member has become durably ineligible.
	membership = s.failoverMembershipSnapshot()
	if membership.JointActive &&
		membership.ConfigEpoch == membership.CommitOldEpoch &&
		membership.PendingEpoch == membership.CommitEpoch {
		if _, err := s.commitFailoverMembership(membership.GroupID, membership.CommitEpoch); err != nil {
			return err
		}
		membership = s.failoverMembershipSnapshot()
	}
	if membership.ConfigEpoch != membership.CommitEpoch || membership.JointActive {
		return nil
	}

	allConverged := true
	for _, addr := range membership.CommitTargets {
		if addr == s.failoverAdvertiseAddr {
			continue
		}
		state, err := queryFailoverPeer(
			addr,
			300*time.Millisecond,
			s.replicationMasterUser,
			s.replicationMasterAuth,
			s.clusterControlAuth,
		)
		if err == nil &&
			state.GroupID == membership.GroupID &&
			state.ConfigEpoch >= membership.CommitEpoch &&
			!state.JointActive {
			continue
		}

		if err == nil &&
			state.GroupID == membership.GroupID &&
			state.ConfigEpoch == membership.CommitOldEpoch &&
			!state.JointActive {
			reply, prepErr := queryFailoverMembershipPrepare(
				addr,
				300*time.Millisecond,
				s.replicationMasterUser,
				s.replicationMasterAuth,
				membership.GroupID,
				membership.CommitOldEpoch,
				membership.CommitEpoch,
				membership.CommitMembers,
				membership.CommitQuorum,
			)
			if prepErr != nil || !reply.Accepted {
				allConverged = false
				continue
			}
		}

		commitReply, commitErr := queryFailoverMembershipCommit(
			addr,
			300*time.Millisecond,
			s.replicationMasterUser,
			s.replicationMasterAuth,
			membership.GroupID,
			membership.CommitEpoch,
			s.clusterControlAuth,
		)
		if commitErr != nil ||
			commitReply.ConfigEpoch < membership.CommitEpoch ||
			commitReply.Joint {
			allConverged = false
		}
	}

	if allConverged {
		return s.clearFailoverMembershipCommitRecovery()
	}
	return nil
}

func (s *Server) coordinateFailoverMembershipChange(newEpoch uint64, newMembers []string, newQuorum int) (failoverMembershipChangeResult, error) {
	membership := s.failoverMembershipSnapshot()
	result := failoverMembershipChangeResult{
		OldQuorum: membership.Quorum,
		NewQuorum: newQuorum,
	}
	if membership.JointActive || membership.CommitPending {
		return result, errors.New("failover membership transition already active")
	}
	if membership.GroupID == "" || membership.ConfigEpoch == 0 {
		return result, errors.New("dynamic failover membership requires group id and nonzero epoch")
	}
	if newEpoch != membership.ConfigEpoch+1 {
		return result, errors.New("new failover membership epoch must increase by exactly one")
	}
	if s.failoverAdvertiseAddr == "" {
		return result, errors.New("failover_advertise_addr is required for dynamic membership")
	}
	s.replication.mu.RLock()
	role := s.replication.role
	s.replication.mu.RUnlock()
	if role != replicationMaster {
		return result, errors.New("only the current primary may coordinate failover membership")
	}
	if _, err := deriveFailoverPeersForMember(newMembers, s.failoverAdvertiseAddr); err != nil {
		return result, err
	}
	newPeers, _ := deriveFailoverPeersForMember(newMembers, s.failoverAdvertiseAddr)
	if err := validateFailoverPeerSet(newPeers, newQuorum); err != nil {
		return result, err
	}

	oldMembers, err := s.failoverCurrentMembers()
	if err != nil {
		return result, err
	}
	if !containsString(newMembers, s.failoverAdvertiseAddr) {
		return result, errors.New("coordinator must remain in proposed failover membership")
	}

	removed := make([]string, 0)
	for _, member := range oldMembers {
		if !containsString(newMembers, member) {
			removed = append(removed, member)
		}
	}

	localReply, err := s.prepareFailoverMembershipMembers(
		membership.GroupID,
		membership.ConfigEpoch,
		newEpoch,
		newMembers,
		newQuorum,
	)
	if err != nil {
		return result, err
	}
	if !localReply.Accepted {
		return result, errors.New("local failover membership prepare rejected")
	}

	result.OldAcks++
	result.NewAcks++
	prepared := map[string]struct{}{s.failoverAdvertiseAddr: {}}

	// Only members that remain in the proposed configuration enter joint state.
	// Removed members instead prepare a retirement tombstone.
	for _, addr := range newMembers {
		if addr == s.failoverAdvertiseAddr {
			continue
		}
		reply, err := queryFailoverMembershipPrepare(
			addr,
			300*time.Millisecond,
			s.replicationMasterUser,
			s.replicationMasterAuth,
			membership.GroupID,
			membership.ConfigEpoch,
			newEpoch,
			newMembers,
			newQuorum,
			s.clusterControlAuth,
		)
		if err != nil || !reply.Accepted || !reply.Joint || reply.PendingEpoch != newEpoch {
			continue
		}
		prepared[addr] = struct{}{}
		result.PreparedPeers = append(result.PreparedPeers, addr)
		if containsString(oldMembers, addr) {
			result.OldAcks++
		}
		result.NewAcks++
	}

	// Do not stage irreversible retirement unless the proposed membership has
	// already reached its own majority and the old majority is reachable once
	// retiring members contribute their explicit consent.
	if result.NewAcks < newQuorum || result.OldAcks+len(removed) < membership.Quorum {
		for addr := range prepared {
			if addr == s.failoverAdvertiseAddr {
				continue
			}
			_, _ = queryFailoverMembershipAbort(
				addr,
				300*time.Millisecond,
				s.replicationMasterUser,
				s.replicationMasterAuth,
				membership.GroupID,
				newEpoch,
				s.clusterControlAuth,
			)
		}
		_, _ = s.abortFailoverMembership(membership.GroupID, newEpoch)
		return result, nil
	}

	retirePrepared := make([]string, 0, len(removed))
	for _, addr := range removed {
		reply, err := queryFailoverRetirePrepare(
			addr,
			300*time.Millisecond,
			s.replicationMasterUser,
			s.replicationMasterAuth,
			membership.GroupID,
			membership.ConfigEpoch,
			newEpoch,
			s.clusterControlAuth,
		)
		if err != nil || !reply.Accepted {
			continue
		}
		retirePrepared = append(retirePrepared, addr)
		result.OldAcks++
	}

	if result.OldAcks < membership.Quorum {
		for addr := range prepared {
			if addr == s.failoverAdvertiseAddr {
				continue
			}
			_, _ = queryFailoverMembershipAbort(
				addr,
				300*time.Millisecond,
				s.replicationMasterUser,
				s.replicationMasterAuth,
				membership.GroupID,
				newEpoch,
				s.clusterControlAuth,
			)
		}
		_, _ = s.abortFailoverMembership(membership.GroupID, newEpoch)
		return result, nil
	}

	result.Prepared = true
	if err := s.armFailoverMembershipCommitRecovery(
		membership.ConfigEpoch,
		newEpoch,
		newMembers,
		newQuorum,
		append([]string(nil), newMembers...),
		retirePrepared,
	); err != nil {
		return result, err
	}

	if err := s.retryFailoverMembershipCommit(time.Now().Add(time.Second)); err != nil {
		return result, err
	}
	after := s.failoverMembershipSnapshot()
	result.Committed = after.ConfigEpoch == newEpoch && !after.JointActive
	return result, nil
}

type failoverRetireReply struct {
	GroupID        string `json:"group_id"`
	ConfigEpoch    uint64 `json:"config_epoch"`
	Retired        bool   `json:"retired"`
	RetiredAtEpoch uint64 `json:"retired_at_epoch,omitempty"`
	Accepted       bool   `json:"accepted"`
}


func (s *Server) prepareFailoverRetirement(groupID string, currentEpoch, retireAtEpoch uint64) (failoverRetireReply, error) {
	if groupID == "" || retireAtEpoch <= currentEpoch {
		return failoverRetireReply{}, errors.New("invalid failover retirement epoch")
	}
	if _, ok := replicationPersistencePaths.Load(s); !ok {
		return failoverRetireReply{}, errors.New("failover membership persistence is unavailable")
	}

	s.failoverMembershipMu.Lock()
	if s.failoverGroupID != groupID || s.failoverConfigEpoch != currentEpoch {
		reply := failoverRetireReply{
			GroupID: s.failoverGroupID,
			ConfigEpoch: s.failoverConfigEpoch,
			Retired: s.failoverRetired,
			RetiredAtEpoch: s.failoverRetiredAtEpoch,
		}
		s.failoverMembershipMu.Unlock()
		return reply, nil
	}
	if s.failoverRetired {
		reply := failoverRetireReply{
			GroupID: groupID,
			ConfigEpoch: currentEpoch,
			Retired: true,
			RetiredAtEpoch: s.failoverRetiredAtEpoch,
			Accepted: s.failoverRetiredAtEpoch == retireAtEpoch,
		}
		s.failoverMembershipMu.Unlock()
		return reply, nil
	}
	if s.failoverRetirePending {
		reply := failoverRetireReply{
			GroupID: groupID,
			ConfigEpoch: currentEpoch,
			Retired: false,
			RetiredAtEpoch: s.failoverRetirePendingEpoch,
			Accepted: s.failoverRetirePendingEpoch == retireAtEpoch,
		}
		s.failoverMembershipMu.Unlock()
		return reply, nil
	}
	s.failoverRetirePending = true
	s.failoverRetirePendingEpoch = retireAtEpoch
	s.failoverMembershipMu.Unlock()

	if err := s.persistFailoverMembershipState(); err != nil {
		s.failoverMembershipMu.Lock()
		s.failoverRetirePending = false
		s.failoverRetirePendingEpoch = 0
		s.failoverMembershipMu.Unlock()
		return failoverRetireReply{}, err
	}
	return failoverRetireReply{
		GroupID: groupID,
		ConfigEpoch: currentEpoch,
		RetiredAtEpoch: retireAtEpoch,
		Accepted: true,
	}, nil
}

func (s *Server) retireFailoverMember(groupID string, currentEpoch, retireAtEpoch uint64) (failoverRetireReply, error) {
	if groupID == "" || retireAtEpoch <= currentEpoch {
		return failoverRetireReply{}, errors.New("invalid failover retirement epoch")
	}
	if _, ok := replicationPersistencePaths.Load(s); !ok {
		return failoverRetireReply{}, errors.New("failover membership persistence is unavailable")
	}

	s.failoverMembershipMu.Lock()
	if s.failoverGroupID != groupID || s.failoverConfigEpoch != currentEpoch {
		reply := failoverRetireReply{
			GroupID: s.failoverGroupID,
			ConfigEpoch: s.failoverConfigEpoch,
			Retired: s.failoverRetired,
			RetiredAtEpoch: s.failoverRetiredAtEpoch,
		}
		s.failoverMembershipMu.Unlock()
		return reply, nil
	}
	if s.failoverRetired {
		reply := failoverRetireReply{
			GroupID: groupID,
			ConfigEpoch: currentEpoch,
			Retired: true,
			RetiredAtEpoch: s.failoverRetiredAtEpoch,
			Accepted: s.failoverRetiredAtEpoch == retireAtEpoch,
		}
		s.failoverMembershipMu.Unlock()
		return reply, nil
	}
	if !s.failoverRetirePending || s.failoverRetirePendingEpoch != retireAtEpoch {
		s.failoverMembershipMu.Unlock()
		return failoverRetireReply{}, errors.New("failover retirement was not prepared")
	}
	s.failoverRetired = true
	s.failoverRetiredAtEpoch = retireAtEpoch
	s.failoverRetirePending = false
	s.failoverRetirePendingEpoch = 0
	s.failoverMembershipMu.Unlock()

	if err := s.persistFailoverMembershipState(); err != nil {
		s.failoverMembershipMu.Lock()
		s.failoverRetired = false
		s.failoverRetiredAtEpoch = 0
		s.failoverRetirePending = true
		s.failoverRetirePendingEpoch = retireAtEpoch
		s.failoverMembershipMu.Unlock()
		return failoverRetireReply{}, err
	}
	return failoverRetireReply{
		GroupID: groupID,
		ConfigEpoch: currentEpoch,
		Retired: true,
		RetiredAtEpoch: retireAtEpoch,
		Accepted: true,
	}, nil
}

func queryFailoverRetire(addr string, timeout time.Duration, username, password, groupID string, currentEpoch, retireAtEpoch uint64, controlAuth ...string) (failoverRetireReply, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return failoverRetireReply{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	reader := bufio.NewReader(conn)
	if err := authenticateInternalControlUpstream(conn, reader, username, password, internalControlSecret(controlAuth)); err != nil {
		return failoverRetireReply{}, err
	}
	if err := writeReplicationRESPCommand(
		conn,
		"SNUG.FAILOVER", "RETIRE",
		groupID,
		strconv.FormatUint(currentEpoch, 10),
		strconv.FormatUint(retireAtEpoch, 10),
	); err != nil {
		return failoverRetireReply{}, err
	}
	payload, err := readRESPBulk(reader)
	if err != nil {
		return failoverRetireReply{}, err
	}
	var reply failoverRetireReply
	if err := json.Unmarshal(payload, &reply); err != nil {
		return failoverRetireReply{}, err
	}
	return reply, nil
}


func queryFailoverRetirePrepare(addr string, timeout time.Duration, username, password, groupID string, currentEpoch, retireAtEpoch uint64, controlAuth ...string) (failoverRetireReply, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return failoverRetireReply{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	reader := bufio.NewReader(conn)
	if err := authenticateInternalControlUpstream(conn, reader, username, password, internalControlSecret(controlAuth)); err != nil {
		return failoverRetireReply{}, err
	}
	if err := writeReplicationRESPCommand(
		conn,
		"SNUG.FAILOVER", "RETIREPREPARE",
		groupID,
		strconv.FormatUint(currentEpoch, 10),
		strconv.FormatUint(retireAtEpoch, 10),
	); err != nil {
		return failoverRetireReply{}, err
	}
	payload, err := readRESPBulk(reader)
	if err != nil {
		return failoverRetireReply{}, err
	}
	var reply failoverRetireReply
	if err := json.Unmarshal(payload, &reply); err != nil {
		return failoverRetireReply{}, err
	}
	return reply, nil
}
