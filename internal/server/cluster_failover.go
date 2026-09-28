package server

import (
	"errors"
	"fmt"
	"net"
	"strconv"
)

func (s *Server) clusterFailoverShardMembers() []string {
	membership := s.failoverMembershipSnapshot()
	members := make([]string, 0, len(membership.Peers)+1)
	if s.failoverAdvertiseAddr != "" {
		members = append(members, s.failoverAdvertiseAddr)
	}
	members = append(members, membership.Peers...)
	return members
}

func (s *Server) clusterFailoverCurrentOwner(state clusterStateSnapshot) (string, error) {
	members := s.clusterFailoverShardMembers()
	memberSet := make(map[string]struct{}, len(members))
	for _, member := range members {
		memberSet[member] = struct{}{}
	}

	owner := ""
	for _, slotOwner := range state.owners {
		if slotOwner == "" {
			continue
		}
		if _, ok := memberSet[slotOwner]; !ok {
			continue
		}
		if owner == "" {
			owner = slotOwner
			continue
		}
		if owner != slotOwner {
			return "", errors.New("ERR cluster failover ownership is ambiguous across multiple shard members")
		}
	}
	return owner, nil
}

func validateClusterOwnerAddress(addr string) error {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil || host == "" || portText == "" {
		return errors.New("ERR invalid cluster failover owner address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return errors.New("ERR invalid cluster failover owner address")
	}
	return nil
}

func (s *Server) replaceClusterOwner(oldOwner, newOwner string) error {
	if oldOwner == "" || newOwner == "" || oldOwner == newOwner {
		return errors.New("ERR invalid cluster failover owner replacement")
	}
	if err := validateClusterOwnerAddress(oldOwner); err != nil {
		return err
	}
	if err := validateClusterOwnerAddress(newOwner); err != nil {
		return err
	}

	s.clusterMu.Lock()
	defer s.clusterMu.Unlock()
	if !s.clusterEnabled {
		return errors.New("ERR This instance has cluster support disabled")
	}
	for slot := 0; slot < clusterSlotCount; slot++ {
		if s.clusterSlotMigrating[slot] != "" || s.clusterSlotImporting[slot] != "" {
			return errors.New("ERR cluster failover ownership refused while slots are migrating or importing")
		}
	}

	foundOld := false
	foundNew := false
	for _, owner := range s.clusterSlotOwners {
		if owner == oldOwner {
			foundOld = true
		}
		if owner == newOwner {
			foundNew = true
		}
	}
	if !foundOld {
		if foundNew {
			return nil
		}
		return errors.New("ERR cluster failover old owner does not own any slots")
	}

	return s.mutateClusterTopologyLocked(func() error {
		for slot := 0; slot < clusterSlotCount; slot++ {
			if s.clusterSlotOwners[slot] == oldOwner {
				s.clusterSlotOwners[slot] = newOwner
			}
		}
		return nil
	})
}

func (s *Server) repairClusterFailoverMemberOwnership(newOwner string) error {
	state := s.clusterStateSnapshot()
	if !state.enabled {
		return nil
	}
	oldOwner, err := s.clusterFailoverCurrentOwner(state)
	if err != nil {
		return err
	}
	if oldOwner == "" || oldOwner == newOwner {
		return nil
	}
	return s.replaceClusterOwner(oldOwner, newOwner)
}

func (s *Server) convergeClusterFailoverOwnership() error {
	state := s.clusterStateSnapshot()
	if !state.enabled {
		return nil
	}
	if s.failoverAdvertiseAddr == "" {
		return errors.New("ERR cluster failover requires failover_advertise_addr")
	}
	if state.nodeAddr != s.failoverAdvertiseAddr {
		return fmt.Errorf(
			"ERR cluster failover requires cluster_node_addr %s to match failover_advertise_addr %s",
			state.nodeAddr,
			s.failoverAdvertiseAddr,
		)
	}

	oldOwner, err := s.clusterFailoverCurrentOwner(state)
	if err != nil {
		return err
	}
	if oldOwner == "" || oldOwner == state.nodeAddr {
		return nil
	}
	for slot := 0; slot < clusterSlotCount; slot++ {
		if state.migrating[slot] != "" || state.importing[slot] != "" {
			return errors.New("ERR cluster failover ownership refused while slots are migrating or importing")
		}
	}

	// Converge existing cluster-owner views first. The promoted node updates
	// itself last so a partial control-plane failure remains observable there.
	for _, owner := range clusterOwnersFromOwners(state.owners) {
		if owner == state.nodeAddr || owner == oldOwner {
			continue
		}
		if err := s.sendClusterControlCommand(
			owner,
			"CLUSTER", "FAILOVER-OWNER", oldOwner, state.nodeAddr,
		); err != nil {
			return fmt.Errorf("ERR cluster failover ownership convergence failed for %s: %w", owner, err)
		}
	}

	if err := s.replaceClusterOwner(oldOwner, state.nodeAddr); err != nil {
		return err
	}
	return nil
}

func (s *Server) executeClusterFailoverOwner(args [][]byte) ([]byte, error) {
	if len(args) != 4 {
		return nil, errors.New("ERR wrong number of arguments for 'cluster|failover-owner' command")
	}
	if err := s.replaceClusterOwner(string(args[2]), string(args[3])); err != nil {
		return nil, err
	}
	return []byte("+OK\r\n"), nil
}
