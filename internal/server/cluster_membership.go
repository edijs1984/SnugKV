package server

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
)

func clusterOwnershipDigest(state clusterStateSnapshot) string {
	h := sha1.New()
	for slot, owner := range state.owners {
		fmt.Fprintf(h, "%d=%s\n", slot, owner)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func validateClusterNodeAddress(addr string) error {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil || host == "" || portText == "" {
		return errors.New("ERR cluster node address must be host:port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return errors.New("ERR cluster node address must use a valid port")
	}
	return nil
}

func (s *Server) clusterMembershipCheckSelf(addr, digest string) error {
	state := s.clusterStateSnapshot()
	if !state.enabled {
		return errors.New("ERR This instance has cluster support disabled")
	}
	if state.nodeAddr != addr {
		return fmt.Errorf("ERR cluster join candidate identity mismatch: local=%s expected=%s", state.nodeAddr, addr)
	}
	if clusterRebalanceHasActiveTransition(state) {
		return errors.New("ERR cluster membership change refused while slots are migrating or importing")
	}
	if clusterOwnershipDigest(state) != digest {
		return errors.New("ERR cluster join candidate topology does not match")
	}
	return nil
}

func (s *Server) addClusterKnownNode(addr, digest string) error {
	if err := validateClusterNodeAddress(addr); err != nil {
		return err
	}

	s.clusterMu.Lock()
	defer s.clusterMu.Unlock()
	if !s.clusterEnabled {
		return errors.New("ERR This instance has cluster support disabled")
	}
	state := clusterStateSnapshot{
		enabled:    s.clusterEnabled,
		nodeAddr:   s.clusterNodeAddr,
		epoch:      s.clusterTopologyEpoch,
		knownNodes: sortedClusterKnownNodes(s.clusterKnownNodes),
		owners:     s.clusterSlotOwners,
		migrating:  s.clusterSlotMigrating,
		importing:  s.clusterSlotImporting,
	}
	if clusterRebalanceHasActiveTransition(state) {
		return errors.New("ERR cluster membership change refused while slots are migrating or importing")
	}
	if clusterOwnershipDigest(state) != digest {
		return errors.New("ERR cluster membership topology does not match")
	}
	if s.clusterKnownNodes == nil {
		s.clusterKnownNodes = make(map[string]struct{})
	}
	if _, exists := s.clusterKnownNodes[addr]; exists {
		return nil
	}
	return s.mutateClusterTopologyLocked(func() error {
		s.clusterKnownNodes[addr] = struct{}{}
		return nil
	})
}

func (s *Server) executeClusterMembership(args [][]byte) ([]byte, error) {
	if len(args) < 3 {
		return nil, errors.New("ERR syntax error")
	}
	switch stringUpper(args[2]) {
	case "CHECK":
		if len(args) != 5 {
			return nil, errors.New("ERR syntax error")
		}
		if err := s.clusterMembershipCheckSelf(string(args[3]), string(args[4])); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil

	case "ADD":
		if len(args) != 5 {
			return nil, errors.New("ERR syntax error")
		}
		if err := s.addClusterKnownNode(string(args[3]), string(args[4])); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil

	default:
		return nil, errors.New("ERR syntax error")
	}
}

func stringUpper(value []byte) string {
	b := append([]byte(nil), value...)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 'a' - 'A'
		}
	}
	return string(b)
}

func (s *Server) executeClusterJoin(args [][]byte) ([]byte, error) {
	if len(args) != 3 {
		return nil, errors.New("ERR wrong number of arguments for 'cluster|join' command")
	}
	addr := string(args[2])
	if err := validateClusterNodeAddress(addr); err != nil {
		return nil, err
	}

	release, err := s.beginClusterRebalanceOperation()
	if err != nil {
		return nil, err
	}
	defer release()

	state := s.clusterStateSnapshot()
	if !state.enabled {
		return nil, errors.New("ERR This instance has cluster support disabled")
	}
	if clusterRebalanceHasActiveTransition(state) {
		return nil, errors.New("ERR cluster membership change refused while slots are migrating or importing")
	}
	for _, known := range clusterKnownNodesFromState(state) {
		if known == addr {
			return []byte("+OK\r\n"), nil
		}
	}

	digest := clusterOwnershipDigest(state)
	if err := s.sendClusterControlCommand(
		addr,
		"CLUSTER", "MEMBERSHIP", "CHECK", addr, digest,
	); err != nil {
		return nil, fmt.Errorf("ERR cluster join preflight failed: %w", err)
	}

	members := clusterKnownNodesFromState(state)
	sort.Strings(members)

	// First teach the candidate the complete existing registry. Its configured
	// ownership map already matches; this fills in any existing zero-slot nodes.
	for _, member := range members {
		if err := s.sendClusterControlCommand(
			addr,
			"CLUSTER", "MEMBERSHIP", "ADD", member, digest,
		); err != nil {
			return nil, fmt.Errorf("ERR cluster join candidate membership sync failed: %w", err)
		}
	}
	if err := s.sendClusterControlCommand(
		addr,
		"CLUSTER", "MEMBERSHIP", "ADD", addr, digest,
	); err != nil {
		return nil, fmt.Errorf("ERR cluster join candidate activation failed: %w", err)
	}

	// Propagate the candidate to existing peers before committing locally.
	for _, member := range members {
		if member == state.nodeAddr {
			continue
		}
		if err := s.sendClusterControlCommand(
			member,
			"CLUSTER", "MEMBERSHIP", "ADD", addr, digest,
		); err != nil {
			return nil, fmt.Errorf("ERR cluster join membership convergence failed for %s: %w", member, err)
		}
	}

	if err := s.addClusterKnownNode(addr, digest); err != nil {
		return nil, err
	}
	return []byte("+OK\r\n"), nil
}
