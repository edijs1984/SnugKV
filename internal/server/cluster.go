package server

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

const clusterSlotCount = 16384

func clusterHashKey(key []byte) []byte {
	start := -1
	for i, b := range key {
		if b == '{' {
			start = i + 1
			break
		}
	}
	if start < 0 || start >= len(key) {
		return key
	}
	for i := start; i < len(key); i++ {
		if key[i] == '}' {
			if i == start {
				return key
			}
			return key[start:i]
		}
	}
	return key
}

func clusterCRC16(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func clusterKeySlot(key []byte) int {
	return int(clusterCRC16(clusterHashKey(key)) & (clusterSlotCount - 1))
}

func parseClusterSlotRange(text string) (int, int, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, 0, errors.New("empty cluster slot range")
	}
	if !strings.Contains(text, "-") {
		slot, err := strconv.Atoi(text)
		if err != nil || slot < 0 || slot >= clusterSlotCount {
			return 0, 0, fmt.Errorf("invalid cluster slot %q", text)
		}
		return slot, slot, nil
	}
	parts := strings.Split(text, "-")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid cluster slot range %q", text)
	}
	start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid cluster slot range %q", text)
	}
	end, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || start < 0 || end < 0 || start >= clusterSlotCount || end >= clusterSlotCount || start > end {
		return 0, 0, fmt.Errorf("invalid cluster slot range %q", text)
	}
	return start, end, nil
}

type clusterStateSnapshot struct {
	enabled       bool
	nodeAddr      string
	epoch         uint64
	knownNodes    []string
	capacityNodes []string
	owners        [clusterSlotCount]string
	migrating     [clusterSlotCount]string
	importing     [clusterSlotCount]string
}

func sortedClusterKnownNodes(nodes map[string]struct{}) []string {
	out := make([]string, 0, len(nodes))
	for addr := range nodes {
		if addr != "" {
			out = append(out, addr)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Server) clusterStateSnapshot() clusterStateSnapshot {
	s.clusterMu.RLock()
	state := clusterStateSnapshot{
		enabled:    s.clusterEnabled,
		nodeAddr:   s.clusterNodeAddr,
		epoch:      s.clusterTopologyEpoch,
		knownNodes: sortedClusterKnownNodes(s.clusterKnownNodes),
		owners:     s.clusterSlotOwners,
		migrating:  s.clusterSlotMigrating,
		importing:  s.clusterSlotImporting,
	}
	s.clusterMu.RUnlock()

	state.capacityNodes = clusterKnownNodesFromState(state)
	if topology, err := s.localClusterShardReplicaTopology(state); err == nil && topology.Owner != "" {
		replicas := make(map[string]struct{}, len(topology.Replicas))
		for _, replica := range topology.Replicas {
			replicas[replica] = struct{}{}
		}
		filtered := make([]string, 0, len(state.capacityNodes))
		for _, node := range state.capacityNodes {
			if _, isReplica := replicas[node]; isReplica {
				continue
			}
			filtered = append(filtered, node)
		}
		state.capacityNodes = filtered
	}
	return state
}

func (s *Server) clusterEnabledSnapshot() bool {
	s.clusterMu.RLock()
	defer s.clusterMu.RUnlock()
	return s.clusterEnabled
}

func (s *Server) configureClusterSlots(enabled bool, nodeAddr string, ranges map[string]string) error {
	var owners [clusterSlotCount]string
	for rangeText, owner := range ranges {
		start, end, err := parseClusterSlotRange(rangeText)
		if err != nil {
			return err
		}
		for slot := start; slot <= end; slot++ {
			if owners[slot] != "" {
				return fmt.Errorf("cluster slot %d has multiple owners", slot)
			}
			owners[slot] = owner
		}
	}

	known := make(map[string]struct{})
	if nodeAddr != "" {
		known[nodeAddr] = struct{}{}
	}
	for _, owner := range owners {
		if owner != "" {
			known[owner] = struct{}{}
		}
	}

	s.clusterMu.Lock()
	s.clusterEnabled = enabled
	s.clusterNodeAddr = nodeAddr
	s.clusterTopologyEpoch = 0
	s.clusterKnownNodes = known
	s.clusterSlotOwners = owners
	s.clusterSlotMigrating = [clusterSlotCount]string{}
	s.clusterSlotImporting = [clusterSlotCount]string{}
	s.clusterMu.Unlock()
	return nil
}


func (s *Server) enforceClusterRouting(args [][]byte) error {
	return s.enforceClusterRoutingForClientMode(args, s.executionClient, true)
}

func (s *Server) enforceClusterRoutingForClient(args [][]byte, client *clientSession) error {
	return s.enforceClusterRoutingForClientMode(args, client, true)
}

func (s *Server) previewClusterRoutingForClient(args [][]byte, client *clientSession) error {
	return s.enforceClusterRoutingForClientMode(args, client, false)
}

func (s *Server) enforceClusterRoutingForClientMode(args [][]byte, client *clientSession, consumeAsking bool) error {
	// Keep ordinary command routing off the full clusterStateSnapshot path.
	// That snapshot intentionally copies all 16,384 slot-owner/migration arrays
	// and derives topology metadata for operator commands. Single-slot routing
	// only needs the local node plus the three entries for the command's slot.
	if !s.clusterEnabledSnapshot() {
		return nil
	}
	if len(args) == 0 ||
		strings.EqualFold(string(args[0]), "CLUSTER") ||
		strings.EqualFold(string(args[0]), "ASKING") ||
		strings.EqualFold(string(args[0]), "RESTORE-ASKING") {
		return nil
	}

	keys, err := commandKeys(args)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}

	slot := clusterKeySlot(keys[0].value)
	for _, key := range keys[1:] {
		if clusterKeySlot(key.value) != slot {
			return errors.New("CROSSSLOT Keys in request don't hash to the same slot")
		}
	}

	asking := false
	if client != nil {
		if consumeAsking {
			asking = client.consumeClusterAsking()
		} else {
			asking = client.clusterAskingEnabled()
		}
	}

	s.clusterMu.RLock()
	enabled := s.clusterEnabled
	nodeAddr := s.clusterNodeAddr
	owner := s.clusterSlotOwners[slot]
	migrating := s.clusterSlotMigrating[slot]
	importing := s.clusterSlotImporting[slot]
	s.clusterMu.RUnlock()

	if !enabled {
		return nil
	}
	if owner == "" {
		return errors.New("CLUSTERDOWN Hash slot not served")
	}

	if owner == nodeAddr {
		if migrating != "" && len(keys) == 1 {
			if s.store.Exists([]string{string(keys[0].value)}) == 0 {
				return fmt.Errorf("ASK %d %s", slot, migrating)
			}
		}
		return nil
	}

	if importing != "" && asking {
		return nil
	}

	return fmt.Errorf("MOVED %d %s", slot, owner)
}


type clusterSlotRange struct {
	Start int
	End   int
	Owner string
}

func clusterSlotRangesFromOwners(owners [clusterSlotCount]string) []clusterSlotRange {
	out := make([]clusterSlotRange, 0)
	start := -1
	owner := ""
	for slot := 0; slot < clusterSlotCount; slot++ {
		current := owners[slot]
		if current == owner {
			continue
		}
		if owner != "" {
			out = append(out, clusterSlotRange{Start: start, End: slot - 1, Owner: owner})
		}
		owner = current
		start = slot
	}
	if owner != "" {
		out = append(out, clusterSlotRange{Start: start, End: clusterSlotCount - 1, Owner: owner})
	}
	return out
}

func (s *Server) clusterSlotRanges() []clusterSlotRange {
	return clusterSlotRangesFromOwners(s.clusterStateSnapshot().owners)
}

func clusterNodeEndpointReply(addr string) ([]byte, error) {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, err
	}
	return array(
		formatBulkString([]byte(host)),
		integer(int64(port)),
		formatBulkString([]byte(clusterNodeID(addr))),
	), nil
}

func (s *Server) clusterSlotsReply() ([]byte, error) {
	state := s.clusterStateSnapshot()
	ranges := clusterSlotRangesFromOwners(state.owners)
	items := make([][]byte, 0, len(ranges))
	for _, r := range ranges {
		node, err := clusterNodeEndpointReply(r.Owner)
		if err != nil {
			return nil, err
		}
		items = append(items, array(
			integer(int64(r.Start)),
			integer(int64(r.End)),
			node,
		))
	}
	return array(items...), nil
}

func (s *Server) clusterShardsReply() ([]byte, error) {
	state := s.clusterStateSnapshot()
	masters := clusterSlotCapableNodesFromState(state)
	observation := s.clusterShardTopologyObservation(state)
	topologies := observation.Topologies
	items := make([][]byte, 0, len(masters))

	renderNode := func(addr, role, health string) ([]byte, error) {
		host, portText, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			return nil, err
		}
		return array(
			formatBulkString([]byte("id")),
			formatBulkString([]byte(clusterNodeID(addr))),
			formatBulkString([]byte("endpoint")),
			formatBulkString([]byte(host)),
			formatBulkString([]byte("ip")),
			formatBulkString([]byte(host)),
			formatBulkString([]byte("port")),
			integer(int64(port)),
			formatBulkString([]byte("role")),
			formatBulkString([]byte(role)),
			formatBulkString([]byte("health")),
			formatBulkString([]byte(health)),
		), nil
	}

	for _, owner := range masters {
		slotItems := make([][]byte, 0)
		for _, r := range clusterNodeSlotRangesFromOwners(state.owners, owner) {
			slotItems = append(
				slotItems,
				integer(int64(r.Start)),
				integer(int64(r.End)),
			)
		}

		topology := topologies[owner]
		nodeItems := make([][]byte, 0, 1+len(topology.Replicas))
		masterHealth := clusterObservedNodeHealth(observation, owner)
		masterNode, err := renderNode(owner, "master", masterHealth)
		if err != nil {
			return nil, err
		}
		nodeItems = append(nodeItems, masterNode)

		if topology.Owner == owner {
			for _, replica := range topology.Replicas {
				replicaNode, err := renderNode(
					replica,
					"replica",
					clusterObservedNodeHealth(observation, replica),
				)
				if err != nil {
					return nil, err
				}
				nodeItems = append(nodeItems, replicaNode)
			}
		}

		shard := array(
			formatBulkString([]byte("slots")),
			array(slotItems...),
			formatBulkString([]byte("nodes")),
			array(nodeItems...),
		)
		items = append(items, shard)
	}

	return array(items...), nil
}

func clusterNodeID(addr string) string {
	sum := sha1.Sum([]byte(addr))
	return hex.EncodeToString(sum[:])
}

func clusterOwnersFromOwners(slotOwners [clusterSlotCount]string) []string {
	seen := make(map[string]struct{})
	for _, owner := range slotOwners {
		if owner != "" {
			seen[owner] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for owner := range seen {
		out = append(out, owner)
	}
	sort.Strings(out)
	return out
}

func clusterKnownNodesFromState(state clusterStateSnapshot) []string {
	if len(state.knownNodes) > 0 {
		return append([]string(nil), state.knownNodes...)
	}
	seen := make(map[string]struct{})
	if state.nodeAddr != "" {
		seen[state.nodeAddr] = struct{}{}
	}
	for _, owner := range state.owners {
		if owner != "" {
			seen[owner] = struct{}{}
		}
	}
	return sortedClusterKnownNodes(seen)
}

func clusterSlotCapableNodesFromState(state clusterStateSnapshot) []string {
	if len(state.capacityNodes) > 0 {
		return append([]string(nil), state.capacityNodes...)
	}
	return clusterKnownNodesFromState(state)
}


func (s *Server) clusterOwners() []string {
	return clusterOwnersFromOwners(s.clusterStateSnapshot().owners)
}

func clusterNodeSlotRangesFromOwners(slotOwners [clusterSlotCount]string, owner string) []clusterSlotRange {
	ranges := make([]clusterSlotRange, 0)
	start := -1
	for slot := 0; slot < clusterSlotCount; slot++ {
		match := slotOwners[slot] == owner
		if match && start < 0 {
			start = slot
			continue
		}
		if !match && start >= 0 {
			ranges = append(ranges, clusterSlotRange{Start: start, End: slot - 1, Owner: owner})
			start = -1
		}
	}
	if start >= 0 {
		ranges = append(ranges, clusterSlotRange{Start: start, End: clusterSlotCount - 1, Owner: owner})
	}
	return ranges
}

func (s *Server) clusterNodeSlotRanges(owner string) []clusterSlotRange {
	return clusterNodeSlotRangesFromOwners(s.clusterStateSnapshot().owners, owner)
}

func clusterSlotRangeText(r clusterSlotRange) string {
	if r.Start == r.End {
		return strconv.Itoa(r.Start)
	}
	return fmt.Sprintf("%d-%d", r.Start, r.End)
}

func (s *Server) clusterNodesReply() []byte {
	state := s.clusterStateSnapshot()
	topologies := s.clusterShardReplicaTopologies(state)
	nodes := clusterTopologyNodesFromTopologies(state, topologies)
	replicaMaster := clusterReplicaMasterMapFromTopologies(topologies)

	lines := make([]string, 0, len(nodes))
	for _, node := range nodes {
		masterAddr, isReplica := replicaMaster[node]
		flags := "master"
		masterID := "-"
		if isReplica {
			flags = "slave"
			masterID = clusterNodeID(masterAddr)
		}
		if node == state.nodeAddr {
			if isReplica {
				flags = "myself,slave"
			} else {
				flags = "myself,master"
			}
		}

		parts := []string{
			clusterNodeID(node),
			node + "@0",
			flags,
			masterID,
			"0",
			"0",
			"0",
			"connected",
		}
		if !isReplica {
			for _, r := range clusterNodeSlotRangesFromOwners(state.owners, node) {
				parts = append(parts, clusterSlotRangeText(r))
			}
		}
		if node == state.nodeAddr {
			for slot := 0; slot < clusterSlotCount; slot++ {
				if target := state.migrating[slot]; target != "" {
					parts = append(parts, fmt.Sprintf("[%d->-%s]", slot, clusterNodeID(target)))
				}
				if source := state.importing[slot]; source != "" {
					parts = append(parts, fmt.Sprintf("[%d-<-%s]", slot, clusterNodeID(source)))
				}
			}
		}
		lines = append(lines, strings.Join(parts, " "))
	}
	if len(lines) == 0 {
		return formatBulkString(nil)
	}
	return formatBulkString([]byte(strings.Join(lines, "\n") + "\n"))
}

func (s *Server) clusterInfoReply() []byte {
	stateSnapshot := s.clusterStateSnapshot()
	assigned := 0
	for _, owner := range stateSnapshot.owners {
		if owner != "" {
			assigned++
		}
	}
	state := "fail"
	if assigned == clusterSlotCount {
		state = "ok"
	}
	owners := clusterOwnersFromOwners(stateSnapshot.owners)
	observation := s.clusterShardTopologyObservation(stateSnapshot)
	knownNodes := clusterTopologyNodesFromTopologies(stateSnapshot, observation.Topologies)
	shardsTotal, replicasTotal, replicasOnline, replicasUnknown := clusterShardHealthCounters(stateSnapshot, observation)
	body := fmt.Sprintf(
		"cluster_state:%s\r\n"+
			"cluster_slots_assigned:%d\r\n"+
			"cluster_slots_ok:%d\r\n"+
			"cluster_slots_pfail:0\r\n"+
			"cluster_slots_fail:0\r\n"+
			"cluster_known_nodes:%d\r\n"+
			"cluster_size:%d\r\n"+
			"cluster_shards_total:%d\r\n"+
			"cluster_replicas_total:%d\r\n"+
			"cluster_replicas_online:%d\r\n"+
			"cluster_replicas_unknown:%d\r\n"+
			"cluster_current_epoch:%d\r\n"+
			"cluster_my_epoch:%d\r\n"+
			"cluster_stats_messages_sent:0\r\n"+
			"cluster_stats_messages_received:0\r\n"+
			"total_cluster_links_buffer_limit_exceeded:0\r\n",
		state,
		assigned,
		assigned,
		len(knownNodes),
		len(owners),
		shardsTotal,
		replicasTotal,
		replicasOnline,
		replicasUnknown,
		stateSnapshot.epoch,
		stateSnapshot.epoch,
	)
	return formatBulkString([]byte(body))
}


func clusterNodeAddressByIDFromState(state clusterStateSnapshot, id string) (string, bool) {
	for _, owner := range clusterKnownNodesFromState(state) {
		if clusterNodeID(owner) == id {
			return owner, true
		}
	}
	if state.nodeAddr != "" && clusterNodeID(state.nodeAddr) == id {
		return state.nodeAddr, true
	}
	return "", false
}

func (s *Server) clusterNodeAddressByID(id string) (string, bool) {
	return clusterNodeAddressByIDFromState(s.clusterStateSnapshot(), id)
}

func (s *Server) clusterSlotHasKeys(slot int) bool {
	for _, key := range s.store.Keys("*") {
		if clusterKeySlot([]byte(key)) == slot {
			return true
		}
	}
	return false
}

func (s *Server) clusterLocalKeysInSlot(slot, limit int) []string {
	state := s.clusterStateSnapshot()
	if !state.enabled || slot < 0 || slot >= clusterSlotCount || limit == 0 {
		return nil
	}
	if state.owners[slot] != state.nodeAddr {
		return nil
	}

	keys := s.store.Keys("*")
	out := make([]string, 0)
	for _, key := range keys {
		if clusterKeySlot([]byte(key)) != slot {
			continue
		}
		out = append(out, key)
		if limit >= 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (s *Server) clusterCountKeysInSlot(args [][]byte) ([]byte, error) {
	if !s.clusterEnabledSnapshot() {
		return nil, errors.New("ERR This instance has cluster support disabled")
	}
	if len(args) != 3 {
		return nil, errors.New("ERR wrong number of arguments for 'cluster|countkeysinslot' command")
	}
	slot, err := strconv.Atoi(string(args[2]))
	if err != nil || slot < 0 || slot >= clusterSlotCount {
		return nil, errors.New("ERR Invalid or out of range slot")
	}
	return integer(int64(len(s.clusterLocalKeysInSlot(slot, -1)))), nil
}

func (s *Server) clusterGetKeysInSlot(args [][]byte) ([]byte, error) {
	if !s.clusterEnabledSnapshot() {
		return nil, errors.New("ERR This instance has cluster support disabled")
	}
	if len(args) != 4 {
		return nil, errors.New("ERR wrong number of arguments for 'cluster|getkeysinslot' command")
	}
	slot, err := strconv.Atoi(string(args[2]))
	if err != nil || slot < 0 || slot >= clusterSlotCount {
		return nil, errors.New("ERR Invalid or out of range slot")
	}
	count, err := strconv.Atoi(string(args[3]))
	if err != nil || count < 0 {
		return nil, errors.New("ERR value is not an integer or out of range")
	}

	keys := s.clusterLocalKeysInSlot(slot, count)
	items := make([][]byte, 0, len(keys))
	for _, key := range keys {
		items = append(items, formatBulkString([]byte(key)))
	}
	return array(items...), nil
}

func (s *Server) executeClusterSetSlot(args [][]byte) ([]byte, error) {
	if len(args) < 4 {
		return nil, errors.New("ERR wrong number of arguments for 'cluster|setslot' command")
	}

	slot, err := strconv.Atoi(string(args[2]))
	if err != nil || slot < 0 || slot >= clusterSlotCount {
		return nil, errors.New("ERR Invalid or out of range slot")
	}

	action := strings.ToUpper(string(args[3]))
	if action == "STABLE" && len(args) != 4 {
		return nil, errors.New("ERR wrong number of arguments for 'cluster|setslot' command")
	}
	if action != "STABLE" && len(args) != 5 {
		return nil, errors.New("ERR wrong number of arguments for 'cluster|setslot' command")
	}

	s.clusterMu.Lock()
	defer s.clusterMu.Unlock()

	if !s.clusterEnabled {
		return nil, errors.New("ERR This instance has cluster support disabled")
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

	switch action {
	case "STABLE":
		if err := s.mutateClusterTopologyLocked(func() error {
			s.clusterSlotMigrating[slot] = ""
			s.clusterSlotImporting[slot] = ""
			return nil
		}); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil

	case "MIGRATING":
		target, ok := clusterNodeAddressByIDFromState(state, string(args[4]))
		if !ok {
			return nil, errors.New("ERR I don't know about node specified")
		}
		if s.clusterSlotOwners[slot] != s.clusterNodeAddr {
			return nil, errors.New("ERR I'm not the owner of hash slot")
		}
		if err := s.mutateClusterTopologyLocked(func() error {
			s.clusterSlotMigrating[slot] = target
			s.clusterSlotImporting[slot] = ""
			return nil
		}); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil

	case "IMPORTING":
		source, ok := clusterNodeAddressByIDFromState(state, string(args[4]))
		if !ok {
			return nil, errors.New("ERR I don't know about node specified")
		}
		if s.clusterSlotOwners[slot] == s.clusterNodeAddr {
			return nil, errors.New("ERR I'm already the owner of hash slot")
		}
		if err := s.mutateClusterTopologyLocked(func() error {
			s.clusterSlotImporting[slot] = source
			s.clusterSlotMigrating[slot] = ""
			return nil
		}); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil

	case "NODE":
		owner, ok := clusterNodeAddressByIDFromState(state, string(args[4]))
		if !ok {
			return nil, errors.New("ERR I don't know about node specified")
		}
		if s.clusterSlotOwners[slot] == s.clusterNodeAddr &&
			owner != s.clusterNodeAddr &&
			s.clusterSlotHasKeys(slot) {
			return nil, fmt.Errorf(
				"ERR Can't assign hashslot %d to a different node while I still hold keys for this hash slot.",
				slot,
			)
		}
		if err := s.mutateClusterTopologyLocked(func() error {
			s.clusterSlotOwners[slot] = owner
			s.clusterSlotMigrating[slot] = ""
			s.clusterSlotImporting[slot] = ""
			return nil
		}); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil

	default:
		return nil, errors.New("ERR Invalid CLUSTER SETSLOT action or number of arguments")
	}
}


func parseClusterSlotArguments(args [][]byte, start int) ([]int, error) {
	if len(args) <= start {
		return nil, errors.New("ERR wrong number of arguments for cluster slot command")
	}
	slots := make([]int, 0, len(args)-start)
	seen := make(map[int]struct{}, len(args)-start)
	for _, raw := range args[start:] {
		slot, err := strconv.Atoi(string(raw))
		if err != nil || slot < 0 || slot >= clusterSlotCount {
			return nil, errors.New("ERR Invalid or out of range slot")
		}
		if _, ok := seen[slot]; ok {
			return nil, fmt.Errorf("ERR Slot %d specified multiple times", slot)
		}
		seen[slot] = struct{}{}
		slots = append(slots, slot)
	}
	return slots, nil
}

func (s *Server) executeClusterAddSlots(args [][]byte) ([]byte, error) {
	if len(args) < 3 {
		return nil, errors.New("ERR wrong number of arguments for 'cluster|addslots' command")
	}
	slots, err := parseClusterSlotArguments(args, 2)
	if err != nil {
		return nil, err
	}

	s.clusterMu.Lock()
	defer s.clusterMu.Unlock()
	if !s.clusterEnabled {
		return nil, errors.New("ERR This instance has cluster support disabled")
	}

	for _, slot := range slots {
		if s.clusterSlotOwners[slot] != "" {
			return nil, fmt.Errorf("ERR Slot %d is already busy", slot)
		}
	}
	if err := s.mutateClusterTopologyLocked(func() error {
		for _, slot := range slots {
			s.clusterSlotOwners[slot] = s.clusterNodeAddr
			s.clusterSlotMigrating[slot] = ""
			s.clusterSlotImporting[slot] = ""
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return []byte("+OK\r\n"), nil
}

func (s *Server) executeClusterDelSlots(args [][]byte) ([]byte, error) {
	if len(args) < 3 {
		return nil, errors.New("ERR wrong number of arguments for 'cluster|delslots' command")
	}
	slots, err := parseClusterSlotArguments(args, 2)
	if err != nil {
		return nil, err
	}

	s.clusterMu.Lock()
	defer s.clusterMu.Unlock()
	if !s.clusterEnabled {
		return nil, errors.New("ERR This instance has cluster support disabled")
	}

	for _, slot := range slots {
		if s.clusterSlotOwners[slot] == "" {
			return nil, fmt.Errorf("ERR Slot %d is already unassigned", slot)
		}
		if s.clusterSlotOwners[slot] != s.clusterNodeAddr {
			return nil, fmt.Errorf("ERR Slot %d is not owned by me", slot)
		}
	}
	if err := s.mutateClusterTopologyLocked(func() error {
		for _, slot := range slots {
			s.clusterSlotOwners[slot] = ""
			s.clusterSlotMigrating[slot] = ""
			s.clusterSlotImporting[slot] = ""
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return []byte("+OK\r\n"), nil
}

func (s *Server) executeClusterFlushSlots(args [][]byte) ([]byte, error) {
	if len(args) != 2 {
		return nil, errors.New("ERR wrong number of arguments for 'cluster|flushslots' command")
	}
	if len(s.store.Keys("*")) != 0 {
		return nil, errors.New("ERR DB must be empty to perform CLUSTER FLUSHSLOTS.")
	}

	s.clusterMu.Lock()
	defer s.clusterMu.Unlock()
	if !s.clusterEnabled {
		return nil, errors.New("ERR This instance has cluster support disabled")
	}

	if err := s.mutateClusterTopologyLocked(func() error {
		for slot := 0; slot < clusterSlotCount; slot++ {
			if s.clusterSlotOwners[slot] == s.clusterNodeAddr {
				s.clusterSlotOwners[slot] = ""
			}
			s.clusterSlotMigrating[slot] = ""
			s.clusterSlotImporting[slot] = ""
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return []byte("+OK\r\n"), nil
}

func (s *Server) executeClusterMyID(args [][]byte) ([]byte, error) {
	if len(args) != 2 {
		return nil, errors.New("ERR wrong number of arguments for 'cluster|myid' command")
	}
	state := s.clusterStateSnapshot()
	if !state.enabled {
		return nil, errors.New("ERR This instance has cluster support disabled")
	}
	return formatBulkString([]byte(clusterNodeID(state.nodeAddr))), nil
}


type clusterRebalanceMove struct {
	Start  int
	End    int
	Source string
	Target string
}

func clusterRebalancePlanID(state clusterStateSnapshot, moves []clusterRebalanceMove) string {
	h := sha1.New()
	fmt.Fprintf(h, "node=%s\n", state.nodeAddr)
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

func planClusterRebalance(state clusterStateSnapshot) ([]clusterRebalanceMove, error) {
	if !state.enabled {
		return nil, errors.New("ERR This instance has cluster support disabled")
	}

	owners := clusterSlotCapableNodesFromState(state)
	counts := make(map[string]int, len(owners))
	assigned := 0
	for _, owner := range state.owners {
		if owner == "" {
			continue
		}
		counts[owner]++
		assigned++
	}
	if assigned != clusterSlotCount {
		return nil, errors.New("ERR REBALANCE PLAN requires all hash slots to be assigned")
	}
	if len(owners) < 2 {
		return []clusterRebalanceMove{}, nil
	}

	targets := make(map[string]int, len(owners))
	base := clusterSlotCount / len(owners)
	extra := clusterSlotCount % len(owners)
	for i, owner := range owners {
		targets[owner] = base
		if i < extra {
			targets[owner]++
		}
	}

	type ownerDelta struct {
		owner string
		slots []int
		need  int
	}

	donors := make([]ownerDelta, 0)
	receivers := make([]ownerDelta, 0)
	for _, owner := range owners {
		delta := counts[owner] - targets[owner]
		if delta > 0 {
			slots := make([]int, 0, counts[owner])
			for slot, slotOwner := range state.owners {
				if slotOwner == owner {
					slots = append(slots, slot)
				}
			}
			sort.Sort(sort.Reverse(sort.IntSlice(slots)))
			donors = append(donors, ownerDelta{owner: owner, slots: slots, need: delta})
		} else if delta < 0 {
			receivers = append(receivers, ownerDelta{owner: owner, need: -delta})
		}
	}

	moves := make([]clusterRebalanceMove, 0)
	for ri := range receivers {
		receiver := &receivers[ri]
		for receiver.need > 0 {
			if len(donors) == 0 {
				return nil, errors.New("ERR rebalance planner could not satisfy target distribution")
			}
			donor := &donors[0]
			if donor.need == 0 {
				donors = donors[1:]
				continue
			}

			take := receiver.need
			if donor.need < take {
				take = donor.need
			}
			if take > len(donor.slots) {
				return nil, errors.New("ERR rebalance planner source slot accounting mismatch")
			}

			selected := append([]int(nil), donor.slots[:take]...)
			donor.slots = donor.slots[take:]
			donor.need -= take
			receiver.need -= take

			sort.Ints(selected)
			start := selected[0]
			prev := selected[0]
			for _, slot := range selected[1:] {
				if slot == prev+1 {
					prev = slot
					continue
				}
				moves = append(moves, clusterRebalanceMove{
					Start: start, End: prev, Source: donor.owner, Target: receiver.owner,
				})
				start = slot
				prev = slot
			}
			moves = append(moves, clusterRebalanceMove{
				Start: start, End: prev, Source: donor.owner, Target: receiver.owner,
			})
		}
	}

	sort.Slice(moves, func(i, j int) bool {
		if moves[i].Start != moves[j].Start {
			return moves[i].Start < moves[j].Start
		}
		if moves[i].End != moves[j].End {
			return moves[i].End < moves[j].End
		}
		if moves[i].Source != moves[j].Source {
			return moves[i].Source < moves[j].Source
		}
		return moves[i].Target < moves[j].Target
	})
	return moves, nil
}

func clusterRebalanceHasActiveTransition(state clusterStateSnapshot) bool {
	for slot := 0; slot < clusterSlotCount; slot++ {
		if state.migrating[slot] != "" || state.importing[slot] != "" {
			return true
		}
	}
	return false
}

type clusterRebalanceRecoveryItem struct {
	Slot   int
	Role   string
	Peer   string
	Owner  string
	Action string
}

func clusterRebalanceRecoveryPlan(state clusterStateSnapshot) []clusterRebalanceRecoveryItem {
	items := make([]clusterRebalanceRecoveryItem, 0)
	for slot := 0; slot < clusterSlotCount; slot++ {
		if target := state.migrating[slot]; target != "" {
			items = append(items, clusterRebalanceRecoveryItem{
				Slot:   slot,
				Role:   "source",
				Peer:   target,
				Owner:  state.owners[slot],
				Action: "resume",
			})
		}
		if source := state.importing[slot]; source != "" {
			items = append(items, clusterRebalanceRecoveryItem{
				Slot:   slot,
				Role:   "target",
				Peer:   source,
				Owner:  state.owners[slot],
				Action: "wait_for_source",
			})
		}
	}
	return items
}

func clusterRebalanceRecoveryPlanReply(state clusterStateSnapshot) []byte {
	items := clusterRebalanceRecoveryPlan(state)
	replies := make([][]byte, 0, len(items))
	for _, item := range items {
		replies = append(replies, array(
			formatBulkString([]byte("slot")),
			integer(int64(item.Slot)),
			formatBulkString([]byte("role")),
			formatBulkString([]byte(item.Role)),
			formatBulkString([]byte("peer")),
			formatBulkString([]byte(item.Peer)),
			formatBulkString([]byte("owner")),
			formatBulkString([]byte(item.Owner)),
			formatBulkString([]byte("action")),
			formatBulkString([]byte(item.Action)),
		))
	}
	status := "clean"
	if len(items) > 0 {
		status = "recovery_needed"
	}
	return array(
		formatBulkString([]byte("status")),
		formatBulkString([]byte(status)),
		formatBulkString([]byte("epoch")),
		integer(int64(state.epoch)),
		formatBulkString([]byte("transitions")),
		array(replies...),
	)
}

func boolToInt64(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

func clusterRebalanceStatusReply(state clusterStateSnapshot, moves []clusterRebalanceMove) []byte {
	planID := clusterRebalancePlanID(state, moves)
	owners := clusterSlotCapableNodesFromState(state)

	current := make(map[string]int, len(owners))
	for _, owner := range state.owners {
		if owner != "" {
			current[owner]++
		}
	}

	targets := make(map[string]int, len(owners))
	if len(owners) > 0 {
		base := clusterSlotCount / len(owners)
		extra := clusterSlotCount % len(owners)
		for i, owner := range owners {
			targets[owner] = base
			if i < extra {
				targets[owner]++
			}
		}
	}

	slotsToMove := 0
	for _, move := range moves {
		slotsToMove += move.End - move.Start + 1
	}

	nodeItems := make([][]byte, 0, len(owners))
	for _, owner := range owners {
		nodeItems = append(nodeItems, array(
			formatBulkString([]byte("node_id")),
			formatBulkString([]byte(clusterNodeID(owner))),
			formatBulkString([]byte("addr")),
			formatBulkString([]byte(owner)),
			formatBulkString([]byte("current_slots")),
			integer(int64(current[owner])),
			formatBulkString([]byte("target_slots")),
			integer(int64(targets[owner])),
			formatBulkString([]byte("delta")),
			integer(int64(targets[owner]-current[owner])),
		))
	}

	status := "ready"
	if clusterRebalanceHasActiveTransition(state) {
		status = "transitioning"
	} else if len(moves) == 0 {
		status = "balanced"
	}

	return array(
		formatBulkString([]byte("status")),
		formatBulkString([]byte(status)),
		formatBulkString([]byte("plan_id")),
		formatBulkString([]byte(planID)),
		formatBulkString([]byte("active_transition")),
		integer(boolToInt64(clusterRebalanceHasActiveTransition(state))),
		formatBulkString([]byte("move_ranges")),
		integer(int64(len(moves))),
		formatBulkString([]byte("slots_to_move")),
		integer(int64(slotsToMove)),
		formatBulkString([]byte("nodes")),
		array(nodeItems...),
	)
}

func (s *Server) sendClusterControlCommand(addr string, args ...string) error {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("ERR invalid rebalance target address: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return fmt.Errorf("ERR invalid rebalance target address: %s", addr)
	}

	conn, err := s.dialReplicationUpstream(host, port)
	if err != nil {
		return fmt.Errorf("ERR rebalance target connection failed: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	reader := bufio.NewReader(conn)
	if err := authenticateReplicationUpstream(
		conn,
		reader,
		s.replicationMasterUser,
		s.replicationMasterAuth,
	); err != nil {
		return fmt.Errorf("ERR rebalance target authentication failed: %w", err)
	}
	if s.clusterControlAuth != "" {
		if err := writeReplicationRESPCommand(conn, "SNUG.INTERNAL", "AUTH", s.clusterControlAuth); err != nil {
			return fmt.Errorf("ERR cluster control authentication write failed: %w", err)
		}
		line, err := readMigrateLine(reader)
		if err != nil {
			return fmt.Errorf("ERR cluster control authentication read failed: %w", err)
		}
		if line != "+OK" {
			if strings.HasPrefix(line, "-") {
				return errors.New(strings.TrimPrefix(line, "-"))
			}
			return fmt.Errorf("ERR unexpected cluster control authentication response: %s", line)
		}
	}
	if err := writeReplicationRESPCommand(conn, args...); err != nil {
		return fmt.Errorf("ERR rebalance target write failed: %w", err)
	}

	line, err := readMigrateLine(reader)
	if err != nil {
		return fmt.Errorf("ERR rebalance target read failed: %w", err)
	}
	if strings.HasPrefix(line, "-") {
		return errors.New(strings.TrimPrefix(line, "-"))
	}
	if line != "+OK" {
		return fmt.Errorf("ERR unexpected rebalance target response: %s", line)
	}
	return nil
}

func (s *Server) finalizeRebalanceSlotOwners(state clusterStateSnapshot, slot int, target string) error {
	targetID := clusterNodeID(target)
	owners := clusterKnownNodesFromState(state)

	// Update all remote topology views first. Keep the source in MIGRATING
	// until every reachable peer agrees, so incomplete convergence remains
	// observable and can be retried safely.
	for _, owner := range owners {
		if owner == state.nodeAddr {
			continue
		}
		if err := s.sendClusterControlCommand(
			owner,
			"CLUSTER", "SETSLOT", strconv.Itoa(slot), "NODE", targetID,
		); err != nil {
			return fmt.Errorf("ERR rebalance topology convergence failed for %s: %w", owner, err)
		}
	}

	_, err := s.executeClusterSetSlot([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"), []byte(strconv.Itoa(slot)),
		[]byte("NODE"), []byte(targetID),
	})
	return err
}

func (s *Server) rebalanceMigrateArgs(host, port, key string) [][]byte {
	return s.rebalanceMigrateKeysArgsWithReplace(host, port, []string{key}, false)
}

func (s *Server) rebalanceRecoveryMigrateArgs(host, port, key string) [][]byte {
	return s.rebalanceMigrateKeysArgsWithReplace(host, port, []string{key}, true)
}

func (s *Server) rebalanceMigrateKeysArgs(host, port string, keys []string) [][]byte {
	return s.rebalanceMigrateKeysArgsWithReplace(host, port, keys, false)
}

func (s *Server) rebalanceRecoveryMigrateKeysArgs(host, port string, keys []string) [][]byte {
	return s.rebalanceMigrateKeysArgsWithReplace(host, port, keys, true)
}

func (s *Server) rebalanceMigrateKeysArgsWithReplace(host, port string, keys []string, replace bool) [][]byte {
	args := [][]byte{
		[]byte("MIGRATE"),
		[]byte(host),
		[]byte(port),
		[]byte{},
		[]byte("0"),
		[]byte("5000"),
	}
	if replace {
		args = append(args, []byte("REPLACE"))
	}
	if s.replicationMasterAuth != "" {
		if s.replicationMasterUser != "" {
			args = append(args,
				[]byte("AUTH2"),
				[]byte(s.replicationMasterUser),
				[]byte(s.replicationMasterAuth),
			)
		} else {
			args = append(args,
				[]byte("AUTH"),
				[]byte(s.replicationMasterAuth),
			)
		}
	}
	args = append(args, []byte("KEYS"))
	for _, key := range keys {
		args = append(args, []byte(key))
	}
	return args
}

func (s *Server) rebalanceMoveOneSlot(state clusterStateSnapshot, move clusterRebalanceMove) (int, error) {
	if move.Source != state.nodeAddr {
		return 0, fmt.Errorf("ERR REBALANCE APPLY ONCE must run on source node %s", move.Source)
	}
	slot := move.Start
	targetID := clusterNodeID(move.Target)
	sourceID := clusterNodeID(move.Source)

	if err := s.sendClusterControlCommand(
		move.Target,
		"CLUSTER", "SETSLOT", strconv.Itoa(slot), "IMPORTING", sourceID,
	); err != nil {
		return 0, err
	}

	localStable := func() {
		_, _ = s.executeClusterSetSlot([][]byte{
			[]byte("CLUSTER"), []byte("SETSLOT"), []byte(strconv.Itoa(slot)), []byte("STABLE"),
		})
	}
	remoteStable := func() {
		_ = s.sendClusterControlCommand(move.Target, "CLUSTER", "SETSLOT", strconv.Itoa(slot), "STABLE")
	}
	rollback := func() {
		localStable()
		remoteStable()
	}

	if _, err := s.executeClusterSetSlot([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"), []byte(strconv.Itoa(slot)),
		[]byte("MIGRATING"), []byte(targetID),
	}); err != nil {
		remoteStable()
		return 0, err
	}

	moved := 0
	host, port, err := net.SplitHostPort(move.Target)
	if err != nil {
		rollback()
		return moved, fmt.Errorf("ERR invalid rebalance target address: %w", err)
	}
	for {
		keys := s.clusterLocalKeysInSlot(slot, 64)
		if len(keys) == 0 {
			break
		}

		result, err := s.executeClusterMigrateDurableLocked(
			s.rebalanceMigrateKeysArgs(host, port, keys),
		)
		remaining := s.store.Exists(keys)
		moved += len(keys) - remaining
		if err != nil {
			if moved == 0 {
				rollback()
			}
			return moved, err
		}
		if string(result) != "+OK\r\n" && string(result) != "+NOKEY\r\n" {
			if moved == 0 {
				rollback()
			}
			return moved, fmt.Errorf("ERR unexpected MIGRATE result %q", result)
		}
	}

	if err := s.finalizeRebalanceSlotOwners(state, slot, move.Target); err != nil {
		return moved, err
	}

	return moved, nil
}

func (s *Server) resumeRebalanceSlot(state clusterStateSnapshot, slot int) (int, error) {
	if slot < 0 || slot >= clusterSlotCount {
		return 0, errors.New("ERR Invalid or out of range slot")
	}
	if state.owners[slot] != state.nodeAddr {
		return 0, errors.New("ERR REBALANCE RECOVER RESUME must run on the slot owner")
	}
	target := state.migrating[slot]
	if target == "" {
		return 0, errors.New("ERR REBALANCE RECOVER RESUME requires a locally migrating slot")
	}

	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return 0, fmt.Errorf("ERR invalid rebalance target address: %w", err)
	}

	// Recovery may resume after either node restarted or after an operator
	// cleared an incomplete target-side transition. Reassert IMPORTING before
	// moving data so RESTORE-ASKING remains valid and the split slot is
	// explicitly represented on both sides.
	if err := s.sendClusterControlCommand(
		target,
		"CLUSTER", "SETSLOT", strconv.Itoa(slot), "IMPORTING", clusterNodeID(state.nodeAddr),
	); err != nil {
		return 0, fmt.Errorf("ERR REBALANCE RECOVER RESUME could not re-establish target importing state: %w", err)
	}

	moved := 0
	for {
		keys := s.clusterLocalKeysInSlot(slot, 64)
		if len(keys) == 0 {
			break
		}
		result, err := s.executeClusterMigrateDurableLocked(
			s.rebalanceRecoveryMigrateKeysArgs(host, port, keys),
		)
		remaining := s.store.Exists(keys)
		moved += len(keys) - remaining
		if err != nil {
			return moved, err
		}
		if string(result) != "+OK\r\n" && string(result) != "+NOKEY\r\n" {
			return moved, fmt.Errorf("ERR unexpected MIGRATE result %q", result)
		}
	}

	if err := s.finalizeRebalanceSlotOwners(state, slot, target); err != nil {
		return moved, err
	}
	return moved, nil
}

func (s *Server) executeRemoteRebalanceSlot(slot int, targetID, expectedOwnershipDigest string) error {
	state := s.clusterStateSnapshot()
	if expectedOwnershipDigest == "" || clusterOwnershipDigest(state) != expectedOwnershipDigest {
		return errors.New("ERR rebalance execute topology fence rejected stale coordinator")
	}
	if !state.enabled {
		return errors.New("ERR This instance has cluster support disabled")
	}
	if slot < 0 || slot >= clusterSlotCount {
		return errors.New("ERR Invalid or out of range slot")
	}
	if state.owners[slot] != state.nodeAddr {
		return errors.New("ERR rebalance execute slot is not owned by this node")
	}
	if state.migrating[slot] != "" || state.importing[slot] != "" {
		return errors.New("ERR rebalance execute slot already has an active transition")
	}
	target, ok := clusterNodeAddressByIDFromState(state, targetID)
	if !ok || target == state.nodeAddr {
		return errors.New("ERR rebalance execute target is invalid")
	}
	_, err := s.rebalanceMoveOneSlot(state, clusterRebalanceMove{
		Start: slot,
		End: slot,
		Source: state.nodeAddr,
		Target: target,
	})
	return err
}

func (s *Server) beginClusterRebalanceOperation() (func(), error) {
	if !s.clusterRebalanceMu.TryLock() {
		return nil, errors.New("ERR another cluster rebalance operation is already in progress")
	}
	return s.clusterRebalanceMu.Unlock, nil
}

func (s *Server) executeClusterRebalance(args [][]byte) ([]byte, error) {
	if len(args) < 3 {
		return nil, errors.New("ERR syntax error")
	}

	mode := strings.ToUpper(string(args[2]))
	if mode == "EXECUTE" {
		if len(args) != 6 {
			return nil, errors.New("ERR syntax error")
		}
		release, err := s.beginClusterRebalanceOperation()
		if err != nil {
			return nil, err
		}
		defer release()
		slot, err := strconv.Atoi(string(args[3]))
		if err != nil || slot < 0 || slot >= clusterSlotCount {
			return nil, errors.New("ERR Invalid or out of range slot")
		}
		if err := s.executeRemoteRebalanceSlot(slot, string(args[4]), string(args[5])); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil
	}
	if mode == "RECOVER" {
		if len(args) == 4 && strings.EqualFold(string(args[3]), "PLAN") {
			return clusterRebalanceRecoveryPlanReply(s.clusterStateSnapshot()), nil
		}
		if len(args) == 5 && strings.EqualFold(string(args[3]), "RESUME") {
			release, err := s.beginClusterRebalanceOperation()
			if err != nil {
				return nil, err
			}
			defer release()
			slot, err := strconv.Atoi(string(args[4]))
			if err != nil || slot < 0 || slot >= clusterSlotCount {
				return nil, errors.New("ERR Invalid or out of range slot")
			}
			state := s.clusterStateSnapshot()
			keysMoved, err := s.resumeRebalanceSlot(state, slot)
			if err != nil {
				return nil, err
			}
			return array(
				formatBulkString([]byte("status")),
				formatBulkString([]byte("recovered")),
				formatBulkString([]byte("slot")),
				integer(int64(slot)),
				formatBulkString([]byte("target_addr")),
				formatBulkString([]byte(state.migrating[slot])),
				formatBulkString([]byte("keys_moved")),
				integer(int64(keysMoved)),
			), nil
		}
		return nil, errors.New("ERR syntax error")
	}
	if mode == "STATUS" {
		if len(args) != 3 {
			return nil, errors.New("ERR syntax error")
		}
		state := s.clusterStateSnapshot()
		moves, err := planClusterRebalance(state)
		if err != nil {
			return nil, err
		}
		return clusterRebalanceStatusReply(state, moves), nil
	}
	if mode == "APPLY" {
		if len(args) < 5 {
			return nil, errors.New("ERR syntax error")
		}
		applyMode := strings.ToUpper(string(args[4]))
		if applyMode != "DRYRUN" && applyMode != "ONCE" && applyMode != "BATCH" && applyMode != "ALL" {
			return nil, errors.New("ERR syntax error")
		}
		if (applyMode == "DRYRUN" || applyMode == "ONCE" || applyMode == "ALL") && len(args) != 5 {
			return nil, errors.New("ERR syntax error")
		}
		if applyMode == "BATCH" && len(args) != 6 {
			return nil, errors.New("ERR syntax error")
		}

		if applyMode != "DRYRUN" {
			release, err := s.beginClusterRebalanceOperation()
			if err != nil {
				return nil, err
			}
			defer release()
		}

		state := s.clusterStateSnapshot()
		if clusterRebalanceHasActiveTransition(state) {
			return nil, errors.New("ERR REBALANCE APPLY refused while slots are migrating or importing")
		}

		moves, err := planClusterRebalance(state)
		if err != nil {
			return nil, err
		}
		currentPlanID := clusterRebalancePlanID(state, moves)
		if string(args[3]) != currentPlanID {
			return nil, errors.New("ERR REBALANCE plan is stale; run CLUSTER REBALANCE PLAN again")
		}

		if applyMode == "DRYRUN" {
			return array(
				formatBulkString([]byte("plan_id")),
				formatBulkString([]byte(currentPlanID)),
				formatBulkString([]byte("status")),
				formatBulkString([]byte("ready")),
				formatBulkString([]byte("moves")),
				integer(int64(len(moves))),
			), nil
		}

		if applyMode == "ALL" {
			if len(moves) == 0 {
				return array(
					formatBulkString([]byte("plan_id")),
					formatBulkString([]byte(currentPlanID)),
					formatBulkString([]byte("status")),
					formatBulkString([]byte("balanced")),
					formatBulkString([]byte("slots_moved")),
					integer(0),
				), nil
			}

			slotsMoved := 0
			for _, move := range moves {
				for slot := move.Start; slot <= move.End; slot++ {
					current := s.clusterStateSnapshot()
					if clusterRebalanceHasActiveTransition(current) {
						return array(
							formatBulkString([]byte("plan_id")),
							formatBulkString([]byte(currentPlanID)),
							formatBulkString([]byte("status")),
							formatBulkString([]byte("partial")),
							formatBulkString([]byte("slots_moved")),
							integer(int64(slotsMoved)),
							formatBulkString([]byte("failed_slot")),
							integer(int64(slot)),
							formatBulkString([]byte("error")),
							formatBulkString([]byte("ERR active transition detected during full-plan execution")),
						), nil
					}

					var moveErr error
					if move.Source == current.nodeAddr {
						_, moveErr = s.rebalanceMoveOneSlot(current, clusterRebalanceMove{
							Start: slot,
							End: slot,
							Source: move.Source,
							Target: move.Target,
						})
					} else {
						moveErr = s.sendClusterControlCommand(
							move.Source,
							"CLUSTER", "REBALANCE", "EXECUTE",
							strconv.Itoa(slot),
							clusterNodeID(move.Target),
							clusterOwnershipDigest(current),
						)
					}
					if moveErr != nil {
						if slotsMoved == 0 {
							return nil, moveErr
						}
						return array(
							formatBulkString([]byte("plan_id")),
							formatBulkString([]byte(currentPlanID)),
							formatBulkString([]byte("status")),
							formatBulkString([]byte("partial")),
							formatBulkString([]byte("slots_moved")),
							integer(int64(slotsMoved)),
							formatBulkString([]byte("failed_slot")),
							integer(int64(slot)),
							formatBulkString([]byte("error")),
							formatBulkString([]byte(moveErr.Error())),
						), nil
					}
					slotsMoved++
				}
			}

			return array(
				formatBulkString([]byte("plan_id")),
				formatBulkString([]byte(currentPlanID)),
				formatBulkString([]byte("status")),
				formatBulkString([]byte("complete")),
				formatBulkString([]byte("slots_moved")),
				integer(int64(slotsMoved)),
			), nil
		}

		if applyMode == "BATCH" {
			limit, err := strconv.Atoi(string(args[5]))
			if err != nil || limit <= 0 {
				return nil, errors.New("ERR REBALANCE APPLY BATCH limit must be a positive integer")
			}

			slotsMoved := 0
			keysMoved := 0
			for _, move := range moves {
				if move.Source != state.nodeAddr {
					continue
				}
				for slot := move.Start; slot <= move.End && slotsMoved < limit; slot++ {
					single := clusterRebalanceMove{
						Start: slot,
						End: slot,
						Source: move.Source,
						Target: move.Target,
					}
					moved, moveErr := s.rebalanceMoveOneSlot(state, single)
					keysMoved += moved
					if moveErr != nil {
						if slotsMoved == 0 {
							return nil, moveErr
						}
						return array(
							formatBulkString([]byte("plan_id")),
							formatBulkString([]byte(currentPlanID)),
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
				if slotsMoved >= limit {
					break
				}
			}

			if slotsMoved == 0 && len(moves) > 0 {
				return nil, fmt.Errorf("ERR REBALANCE APPLY BATCH has no planned slots sourced by this node %s", state.nodeAddr)
			}

			status := "moved"
			if len(moves) == 0 {
				status = "balanced"
			}
			return array(
				formatBulkString([]byte("plan_id")),
				formatBulkString([]byte(currentPlanID)),
				formatBulkString([]byte("status")),
				formatBulkString([]byte(status)),
				formatBulkString([]byte("slots_moved")),
				integer(int64(slotsMoved)),
				formatBulkString([]byte("keys_moved")),
				integer(int64(keysMoved)),
			), nil
		}

		if len(moves) == 0 {
			return array(
				formatBulkString([]byte("plan_id")),
				formatBulkString([]byte(currentPlanID)),
				formatBulkString([]byte("status")),
				formatBulkString([]byte("balanced")),
				formatBulkString([]byte("slots_moved")),
				integer(0),
				formatBulkString([]byte("keys_moved")),
				integer(0),
			), nil
		}

		slot := moves[0].Start
		keysMoved, err := s.rebalanceMoveOneSlot(state, moves[0])
		if err != nil {
			return nil, err
		}
		return array(
			formatBulkString([]byte("plan_id")),
			formatBulkString([]byte(currentPlanID)),
			formatBulkString([]byte("status")),
			formatBulkString([]byte("moved")),
			formatBulkString([]byte("slot")),
			integer(int64(slot)),
			formatBulkString([]byte("source_addr")),
			formatBulkString([]byte(moves[0].Source)),
			formatBulkString([]byte("target_addr")),
			formatBulkString([]byte(moves[0].Target)),
			formatBulkString([]byte("slots_moved")),
			integer(1),
			formatBulkString([]byte("keys_moved")),
			integer(int64(keysMoved)),
		), nil
	}

	if mode != "PLAN" || len(args) != 3 {
		return nil, errors.New("ERR syntax error")
	}

	state := s.clusterStateSnapshot()
	moves, err := planClusterRebalance(state)
	if err != nil {
		return nil, err
	}
	planID := clusterRebalancePlanID(state, moves)

	projected := make(map[string]int)
	for _, owner := range state.owners {
		if owner != "" {
			projected[owner]++
		}
	}
	for _, move := range moves {
		count := move.End - move.Start + 1
		projected[move.Source] -= count
		projected[move.Target] += count
	}

	moveItems := make([][]byte, 0, len(moves))
	for _, move := range moves {
		moveItems = append(moveItems, array(
			formatBulkString([]byte("start")),
			integer(int64(move.Start)),
			formatBulkString([]byte("end")),
			integer(int64(move.End)),
			formatBulkString([]byte("source_id")),
			formatBulkString([]byte(clusterNodeID(move.Source))),
			formatBulkString([]byte("source_addr")),
			formatBulkString([]byte(move.Source)),
			formatBulkString([]byte("target_id")),
			formatBulkString([]byte(clusterNodeID(move.Target))),
			formatBulkString([]byte("target_addr")),
			formatBulkString([]byte(move.Target)),
		))
	}

	owners := clusterSlotCapableNodesFromState(state)
	projectedItems := make([][]byte, 0, len(owners))
	for _, owner := range owners {
		projectedItems = append(projectedItems, array(
			formatBulkString([]byte("node_id")),
			formatBulkString([]byte(clusterNodeID(owner))),
			formatBulkString([]byte("addr")),
			formatBulkString([]byte(owner)),
			formatBulkString([]byte("slots")),
			integer(int64(projected[owner])),
		))
	}

	return array(
		formatBulkString([]byte("plan_id")),
		formatBulkString([]byte(planID)),
		formatBulkString([]byte("moves")),
		array(moveItems...),
		formatBulkString([]byte("projected")),
		array(projectedItems...),
	), nil
}
