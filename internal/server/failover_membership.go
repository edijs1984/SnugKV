package server

import (
	"errors"
	"net"
	"strconv"
	"strings"
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

	s.failoverConfigEpoch = s.failoverPendingEpoch
	s.failoverPeers = append([]string(nil), s.failoverPendingPeers...)
	s.failoverQuorum = s.failoverPendingQuorum
	s.failoverJointActive = false
	s.failoverPendingEpoch = 0
	s.failoverPendingPeers = nil
	s.failoverPendingQuorum = 0
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
