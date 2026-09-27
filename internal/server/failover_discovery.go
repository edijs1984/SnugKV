package server

import (
	"encoding/json"
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
