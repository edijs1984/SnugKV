package server

import (
	"encoding/json"
	"errors"
	"sort"
	"time"
)

type failoverDiscoveredPeer struct {
	Address      string            `json:"address"`
	LastSeenUnix int64             `json:"last_seen_unix"`
	State        failoverPeerState `json:"state"`
}

func (s *Server) discoveredFailoverPeers() []failoverDiscoveredPeer {
	s.failoverDiscoveryMu.RLock()
	out := make([]failoverDiscoveredPeer, 0, len(s.failoverDiscovered))
	for _, peer := range s.failoverDiscovered {
		out = append(out, peer)
	}
	s.failoverDiscoveryMu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}

func (s *Server) discoveredFailoverPeersJSON() ([]byte, error) {
	return json.Marshal(s.discoveredFailoverPeers())
}

func (s *Server) refreshFailoverDiscovery(now time.Time) {
	if s.failoverDiscoveryInterval <= 0 {
		return
	}

	s.failoverDiscoveryMu.Lock()
	if !s.failoverDiscoveryLastRun.IsZero() &&
		now.Sub(s.failoverDiscoveryLastRun) < s.failoverDiscoveryInterval {
		s.failoverDiscoveryMu.Unlock()
		return
	}
	s.failoverDiscoveryLastRun = now
	s.failoverDiscoveryMu.Unlock()

	membership := s.failoverMembershipSnapshot()
	if membership.GroupID == "" || membership.ConfigEpoch == 0 {
		return
	}

	queue := unionStrings(s.failoverDiscoverySeeds, membership.Peers)
	seen := make(map[string]struct{}, len(queue))
	for len(queue) > 0 {
		addr := queue[0]
		queue = queue[1:]
		if addr == "" || addr == s.failoverAdvertiseAddr {
			continue
		}
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}

		state, err := queryFailoverPeer(
			addr,
			300*time.Millisecond,
			s.replicationMasterUser,
			s.replicationMasterAuth,
		)
		if err != nil {
			continue
		}
		if state.GroupID != membership.GroupID || state.ConfigEpoch != membership.ConfigEpoch {
			continue
		}
		if state.AdvertiseAddr == "" {
			continue
		}

		s.failoverDiscoveryMu.Lock()
		s.failoverDiscovered[state.AdvertiseAddr] = failoverDiscoveredPeer{
			Address:      state.AdvertiseAddr,
			LastSeenUnix: now.Unix(),
			State:        state,
		}
		s.failoverDiscoveryMu.Unlock()

		for _, member := range state.Members {
			if member == "" || member == s.failoverAdvertiseAddr {
				continue
			}
			if _, ok := seen[member]; !ok {
				queue = append(queue, member)
			}
		}
	}
}


type failoverDiscoveryAdoptionPlan struct {
	CurrentEpoch uint64   `json:"current_epoch"`
	NextEpoch    uint64   `json:"next_epoch"`
	Members      []string `json:"members"`
	Added        []string `json:"added"`
	Quorum       int      `json:"quorum"`
}

func (s *Server) buildFailoverDiscoveryAdoptionPlan(now time.Time, quorum int) (failoverDiscoveryAdoptionPlan, error) {
	membership := s.failoverMembershipSnapshot()
	if membership.GroupID == "" || membership.ConfigEpoch == 0 {
		return failoverDiscoveryAdoptionPlan{}, errors.New("dynamic failover membership requires group id and nonzero epoch")
	}
	current, err := s.failoverCurrentMembers()
	if err != nil {
		return failoverDiscoveryAdoptionPlan{}, err
	}
	if quorum <= 0 {
		return failoverDiscoveryAdoptionPlan{}, errors.New("failover membership quorum must be positive")
	}

	freshFor := 2 * s.failoverDiscoveryInterval
	if freshFor <= 0 {
		freshFor = 10 * time.Second
	}
	cutoff := now.Add(-freshFor).Unix()

	members := append([]string(nil), current...)
	added := make([]string, 0)
	for _, peer := range s.discoveredFailoverPeers() {
		if peer.LastSeenUnix < cutoff ||
			peer.State.GroupID != membership.GroupID ||
			peer.State.ConfigEpoch != membership.ConfigEpoch ||
			peer.State.Retired ||
			peer.Address == "" ||
			containsString(members, peer.Address) {
			continue
		}
		members = append(members, peer.Address)
		added = append(added, peer.Address)
	}
	sort.Strings(members)
	sort.Strings(added)

	if len(added) == 0 {
		return failoverDiscoveryAdoptionPlan{}, errors.New("no fresh discovered peers are eligible for adoption")
	}
	peers, err := deriveFailoverPeersForMember(members, s.failoverAdvertiseAddr)
	if err != nil {
		return failoverDiscoveryAdoptionPlan{}, err
	}
	if err := validateFailoverPeerSet(peers, quorum); err != nil {
		return failoverDiscoveryAdoptionPlan{}, err
	}
	return failoverDiscoveryAdoptionPlan{
		CurrentEpoch: membership.ConfigEpoch,
		NextEpoch:    membership.ConfigEpoch + 1,
		Members:      members,
		Added:        added,
		Quorum:       quorum,
	}, nil
}

func (s *Server) adoptDiscoveredFailoverPeers(now time.Time, quorum int) (failoverMembershipChangeResult, failoverDiscoveryAdoptionPlan, error) {
	plan, err := s.buildFailoverDiscoveryAdoptionPlan(now, quorum)
	if err != nil {
		return failoverMembershipChangeResult{}, failoverDiscoveryAdoptionPlan{}, err
	}
	result, err := s.coordinateFailoverMembershipChange(plan.NextEpoch, plan.Members, plan.Quorum)
	return result, plan, err
}
