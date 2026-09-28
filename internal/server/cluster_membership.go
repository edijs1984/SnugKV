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

	case "REMOVE":
		if len(args) != 5 {
			return nil, errors.New("ERR syntax error")
		}
		if err := s.removeClusterKnownNode(string(args[3]), string(args[4])); err != nil {
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


func planClusterNodeRemoval(state clusterStateSnapshot, removeAddr string) ([]clusterRebalanceMove, error) {
	if !state.enabled {
		return nil, errors.New("ERR This instance has cluster support disabled")
	}
	members := clusterKnownNodesFromState(state)
	found := false
	remaining := make([]string, 0, len(members)-1)
	for _, member := range members {
		if member == removeAddr {
			found = true
			continue
		}
		remaining = append(remaining, member)
	}
	if !found {
		return nil, errors.New("ERR cluster remove target is not a known node")
	}
	if len(remaining) == 0 {
		return nil, errors.New("ERR cluster remove cannot remove the last node")
	}
	if clusterRebalanceHasActiveTransition(state) {
		return nil, errors.New("ERR cluster membership change refused while slots are migrating or importing")
	}

	counts := make(map[string]int, len(remaining))
	for _, owner := range state.owners {
		if owner != "" && owner != removeAddr {
			counts[owner]++
		}
	}

	targetSlots := make([]int, 0)
	for slot, owner := range state.owners {
		if owner == removeAddr {
			targetSlots = append(targetSlots, slot)
		}
	}
	sort.Ints(targetSlots)

	type assignment struct {
		slot   int
		target string
	}
	assignments := make([]assignment, 0, len(targetSlots))
	for _, slot := range targetSlots {
		sort.Slice(remaining, func(i, j int) bool {
			if counts[remaining[i]] != counts[remaining[j]] {
				return counts[remaining[i]] < counts[remaining[j]]
			}
			return remaining[i] < remaining[j]
		})
		target := remaining[0]
		assignments = append(assignments, assignment{slot: slot, target: target})
		counts[target]++
	}

	moves := make([]clusterRebalanceMove, 0)
	if len(assignments) == 0 {
		return moves, nil
	}
	start := assignments[0].slot
	prev := assignments[0].slot
	target := assignments[0].target
	for _, item := range assignments[1:] {
		if item.slot == prev+1 && item.target == target {
			prev = item.slot
			continue
		}
		moves = append(moves, clusterRebalanceMove{
			Start: start, End: prev, Source: removeAddr, Target: target,
		})
		start = item.slot
		prev = item.slot
		target = item.target
	}
	moves = append(moves, clusterRebalanceMove{
		Start: start, End: prev, Source: removeAddr, Target: target,
	})
	return moves, nil
}

func clusterRemovalPlanID(state clusterStateSnapshot, removeAddr string, moves []clusterRebalanceMove) string {
	h := sha1.New()
	fmt.Fprintf(h, "remove=%s\n", removeAddr)
	fmt.Fprintf(h, "epoch=%d\n", state.epoch)
	for _, node := range clusterKnownNodesFromState(state) {
		fmt.Fprintf(h, "node_member=%s\n", node)
	}
	for slot, owner := range state.owners {
		fmt.Fprintf(h, "%d=%s\n", slot, owner)
	}
	for _, move := range moves {
		fmt.Fprintf(h, "move=%d-%d:%s>%s\n", move.Start, move.End, move.Source, move.Target)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Server) removeClusterKnownNode(addr, digest string) error {
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
	for _, owner := range s.clusterSlotOwners {
		if owner == addr {
			return errors.New("ERR cluster remove target still owns hash slots")
		}
	}
	if s.clusterKnownNodes == nil {
		s.clusterKnownNodes = make(map[string]struct{})
	}
	if _, exists := s.clusterKnownNodes[addr]; !exists {
		return nil
	}
	if len(s.clusterKnownNodes) <= 1 {
		return errors.New("ERR cluster remove cannot remove the last node")
	}
	return s.mutateClusterTopologyLocked(func() error {
		delete(s.clusterKnownNodes, addr)
		return nil
	})
}

func clusterRemovalPlanReply(state clusterStateSnapshot, removeAddr string, moves []clusterRebalanceMove) []byte {
	items := make([][]byte, 0, len(moves))
	slots := 0
	for _, move := range moves {
		slots += move.End - move.Start + 1
		items = append(items, array(
			formatBulkString([]byte("start")),
			integer(int64(move.Start)),
			formatBulkString([]byte("end")),
			integer(int64(move.End)),
			formatBulkString([]byte("source_addr")),
			formatBulkString([]byte(move.Source)),
			formatBulkString([]byte("target_addr")),
			formatBulkString([]byte(move.Target)),
		))
	}
	return array(
		formatBulkString([]byte("plan_id")),
		formatBulkString([]byte(clusterRemovalPlanID(state, removeAddr, moves))),
		formatBulkString([]byte("remove_addr")),
		formatBulkString([]byte(removeAddr)),
		formatBulkString([]byte("slots_to_evacuate")),
		integer(int64(slots)),
		formatBulkString([]byte("moves")),
		array(items...),
	)
}

func (s *Server) executeClusterRemove(args [][]byte) ([]byte, error) {
	if len(args) != 4 && len(args) != 5 {
		return nil, errors.New("ERR wrong number of arguments for 'cluster|remove' command")
	}
	removeAddr := string(args[2])
	if err := validateClusterNodeAddress(removeAddr); err != nil {
		return nil, err
	}

	mode := stringUpper(args[3])
	state := s.clusterStateSnapshot()
	moves, err := planClusterNodeRemoval(state, removeAddr)
	if err != nil {
		return nil, err
	}
	planID := clusterRemovalPlanID(state, removeAddr, moves)

	if mode == "PLAN" {
		if len(args) != 4 {
			return nil, errors.New("ERR syntax error")
		}
		return clusterRemovalPlanReply(state, removeAddr, moves), nil
	}
	if mode != "APPLY" || len(args) != 5 {
		return nil, errors.New("ERR syntax error")
	}
	if string(args[4]) != planID {
		return nil, errors.New("ERR CLUSTER REMOVE plan is stale; run CLUSTER REMOVE <addr> PLAN again")
	}

	release, err := s.beginClusterRebalanceOperation()
	if err != nil {
		return nil, err
	}
	defer release()

	// Revalidate after taking the operation lock.
	state = s.clusterStateSnapshot()
	moves, err = planClusterNodeRemoval(state, removeAddr)
	if err != nil {
		return nil, err
	}
	if clusterRemovalPlanID(state, removeAddr, moves) != planID {
		return nil, errors.New("ERR CLUSTER REMOVE plan is stale; run CLUSTER REMOVE <addr> PLAN again")
	}

	slotsMoved := 0
	keysMoved := 0
	for _, move := range moves {
		for slot := move.Start; slot <= move.End; slot++ {
			current := s.clusterStateSnapshot()
			single := clusterRebalanceMove{
				Start: slot,
				End: slot,
				Source: removeAddr,
				Target: move.Target,
			}
			var moved int
			var moveErr error
			if removeAddr == current.nodeAddr {
				moved, moveErr = s.rebalanceMoveOneSlot(current, single)
			} else {
				moveErr = s.sendClusterControlCommand(
					removeAddr,
					"CLUSTER", "REBALANCE", "EXECUTE",
					strconv.Itoa(slot),
					clusterNodeID(move.Target),
				)
			}
			keysMoved += moved
			if moveErr != nil {
				if slotsMoved == 0 {
					return nil, moveErr
				}
				return array(
					formatBulkString([]byte("status")),
					formatBulkString([]byte("partial")),
					formatBulkString([]byte("slots_moved")),
					integer(int64(slotsMoved)),
					formatBulkString([]byte("keys_moved")),
					integer(int64(keysMoved)),
					formatBulkString([]byte("failed_slot")),
					integer(int64(slot)),
					formatBulkString([]byte("error")),
					formatBulkString([]byte(moveErr.Error())),
				), nil
			}
			slotsMoved++
		}
	}

	finalState := s.clusterStateSnapshot()
	for _, owner := range finalState.owners {
		if owner == removeAddr {
			return nil, errors.New("ERR cluster remove evacuation incomplete; target still owns hash slots")
		}
	}
	if clusterRebalanceHasActiveTransition(finalState) {
		return nil, errors.New("ERR cluster remove evacuation incomplete; active transition remains")
	}

	digest := clusterOwnershipDigest(finalState)
	members := clusterKnownNodesFromState(finalState)

	// Remove membership from surviving peers first. The retiring node is updated
	// last so a partial propagation failure remains visible there and is retryable.
	for _, member := range members {
		if member == finalState.nodeAddr || member == removeAddr {
			continue
		}
		if err := s.sendClusterControlCommand(
			member,
			"CLUSTER", "MEMBERSHIP", "REMOVE", removeAddr, digest,
		); err != nil {
			return nil, fmt.Errorf("ERR cluster remove membership convergence failed for %s: %w", member, err)
		}
	}

	if finalState.nodeAddr != removeAddr {
		if err := s.sendClusterControlCommand(
			removeAddr,
			"CLUSTER", "MEMBERSHIP", "REMOVE", removeAddr, digest,
		); err != nil {
			return nil, fmt.Errorf("ERR cluster remove retiring node update failed: %w", err)
		}
	}
	// Commit on the coordinator last. A failed remote propagation therefore
	// leaves the coordinator's membership unchanged and the operation retryable.
	if err := s.removeClusterKnownNode(removeAddr, digest); err != nil {
		return nil, err
	}

	return array(
		formatBulkString([]byte("status")),
		formatBulkString([]byte("removed")),
		formatBulkString([]byte("remove_addr")),
		formatBulkString([]byte(removeAddr)),
		formatBulkString([]byte("slots_moved")),
		integer(int64(slotsMoved)),
		formatBulkString([]byte("keys_moved")),
		integer(int64(keysMoved)),
	), nil
}
