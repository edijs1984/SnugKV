package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"time"
)

type clusterShardReplicaTopology struct {
	Owner    string
	Replicas []string
	GroupID  string
}

func (s *Server) localClusterShardReplicaTopology(state clusterStateSnapshot) (clusterShardReplicaTopology, error) {
	if !state.enabled || s.failoverAdvertiseAddr == "" {
		return clusterShardReplicaTopology{}, nil
	}

	membership := s.failoverMembershipSnapshot()
	members := s.clusterFailoverShardMembers()
	if len(members) == 0 {
		return clusterShardReplicaTopology{}, nil
	}

	owner, err := s.clusterFailoverCurrentOwner(state)
	if err != nil {
		return clusterShardReplicaTopology{}, err
	}
	if owner == "" {
		return clusterShardReplicaTopology{}, nil
	}

	seen := make(map[string]struct{}, len(members))
	replicas := make([]string, 0, len(members)-1)
	for _, member := range members {
		if member == "" || member == owner {
			continue
		}
		if _, exists := seen[member]; exists {
			continue
		}
		seen[member] = struct{}{}
		replicas = append(replicas, member)
	}
	sort.Strings(replicas)

	return clusterShardReplicaTopology{
		Owner:    owner,
		Replicas: replicas,
		GroupID:  membership.GroupID,
	}, nil
}

func (s *Server) clusterReplicaMasterMap(state clusterStateSnapshot) (map[string]string, error) {
	topology, err := s.localClusterShardReplicaTopology(state)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for _, replica := range topology.Replicas {
		if replica == topology.Owner {
			return nil, errors.New("ERR invalid cluster shard topology: owner listed as replica")
		}
		out[replica] = topology.Owner
	}
	return out, nil
}

func clusterTopologyNodeHealth(localAddr, addr string) string {
	if addr == localAddr {
		return "online"
	}
	// Phase 19 topology reporting intentionally does not invent liveness for a
	// remote failover member. Dedicated failover health remains authoritative.
	return "unknown"
}


func (s *Server) clusterSlotCapableNodes(state clusterStateSnapshot) ([]string, error) {
	replicaMaster, err := s.clusterReplicaMasterMap(state)
	if err != nil {
		return nil, err
	}
	nodes := clusterKnownNodesFromState(state)
	out := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if _, isReplica := replicaMaster[node]; isReplica {
			continue
		}
		out = append(out, node)
	}
	sort.Strings(out)
	return out, nil
}

func (s *Server) clusterTopologyNodes(state clusterStateSnapshot) ([]string, error) {
	nodes := clusterKnownNodesFromState(state)
	topology, err := s.localClusterShardReplicaTopology(state)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(nodes)+len(topology.Replicas))
	for _, node := range nodes {
		seen[node] = struct{}{}
	}
	for _, replica := range topology.Replicas {
		seen[replica] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for node := range seen {
		out = append(out, node)
	}
	sort.Strings(out)
	return out, nil
}


func clusterShardReplicaTopologyFromPeerState(state clusterStateSnapshot, peer failoverPeerState) (clusterShardReplicaTopology, error) {
	if peer.AdvertiseAddr == "" || len(peer.Members) == 0 {
		return clusterShardReplicaTopology{}, nil
	}
	memberSet := make(map[string]struct{}, len(peer.Members))
	for _, member := range peer.Members {
		if member != "" {
			memberSet[member] = struct{}{}
		}
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
			return clusterShardReplicaTopology{}, errors.New("ERR cluster shard topology is ambiguous across multiple owners")
		}
	}
	if owner == "" {
		return clusterShardReplicaTopology{}, nil
	}

	replicas := make([]string, 0, len(memberSet)-1)
	for member := range memberSet {
		if member == owner {
			continue
		}
		replicas = append(replicas, member)
	}
	sort.Strings(replicas)
	return clusterShardReplicaTopology{
		Owner:    owner,
		Replicas: replicas,
		GroupID:  peer.GroupID,
	}, nil
}

func (s *Server) queryClusterFailoverState(addr string) (failoverPeerState, error) {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return failoverPeerState{}, fmt.Errorf("invalid shard owner address: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return failoverPeerState{}, errors.New("invalid shard owner port")
	}

	conn, err := s.dialReplicationUpstream(host, port)
	if err != nil {
		return failoverPeerState{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(750 * time.Millisecond))

	reader := bufio.NewReader(conn)
	if err := authenticateReplicationUpstream(
		conn,
		reader,
		s.replicationMasterUser,
		s.replicationMasterAuth,
	); err != nil {
		return failoverPeerState{}, err
	}
	if err := authenticateInternalControlUpstream(conn, reader, s.clusterControlAuth); err != nil {
		return failoverPeerState{}, err
	}
	if err := writeReplicationRESPCommand(conn, "SNUG.FAILOVER", "STATE"); err != nil {
		return failoverPeerState{}, err
	}
	payload, err := readRESPBulk(reader)
	if err != nil {
		return failoverPeerState{}, err
	}
	var peer failoverPeerState
	if err := json.Unmarshal(payload, &peer); err != nil {
		return failoverPeerState{}, err
	}
	if peer.AdvertiseAddr == "" || (peer.Role != "master" && peer.Role != "replica") {
		return failoverPeerState{}, errors.New("invalid failover topology state")
	}
	return peer, nil
}

func (s *Server) clusterShardReplicaTopologies(state clusterStateSnapshot) map[string]clusterShardReplicaTopology {
	out := make(map[string]clusterShardReplicaTopology)

	local, err := s.localClusterShardReplicaTopology(state)
	if err == nil && local.Owner != "" {
		out[local.Owner] = local
	}

	for _, owner := range clusterOwnersFromOwners(state.owners) {
		if _, exists := out[owner]; exists {
			continue
		}
		peer, err := s.queryClusterFailoverState(owner)
		if err != nil {
			continue
		}
		topology, err := clusterShardReplicaTopologyFromPeerState(state, peer)
		if err != nil || topology.Owner == "" || topology.Owner != owner {
			continue
		}
		out[owner] = topology
	}
	return out
}

func clusterReplicaMasterMapFromTopologies(topologies map[string]clusterShardReplicaTopology) map[string]string {
	out := make(map[string]string)
	for owner, topology := range topologies {
		for _, replica := range topology.Replicas {
			if replica == owner {
				continue
			}
			if _, exists := out[replica]; !exists {
				out[replica] = owner
			}
		}
	}
	return out
}

func clusterTopologyNodesFromTopologies(state clusterStateSnapshot, topologies map[string]clusterShardReplicaTopology) []string {
	seen := make(map[string]struct{})
	for _, node := range clusterKnownNodesFromState(state) {
		seen[node] = struct{}{}
	}
	for _, topology := range topologies {
		for _, replica := range topology.Replicas {
			seen[replica] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for node := range seen {
		out = append(out, node)
	}
	sort.Strings(out)
	return out
}


type clusterShardTopologyObservation struct {
	Topologies map[string]clusterShardReplicaTopology
	Health     map[string]string
}

func (s *Server) clusterShardTopologyObservation(state clusterStateSnapshot) clusterShardTopologyObservation {
	obs := clusterShardTopologyObservation{
		Topologies: make(map[string]clusterShardReplicaTopology),
		Health:     make(map[string]string),
	}
	if state.nodeAddr != "" {
		obs.Health[state.nodeAddr] = "online"
	}

	local, err := s.localClusterShardReplicaTopology(state)
	if err == nil && local.Owner != "" {
		obs.Topologies[local.Owner] = local
		if local.Owner == state.nodeAddr {
			obs.Health[local.Owner] = "online"
		}
	}

	for _, owner := range clusterOwnersFromOwners(state.owners) {
		if owner == state.nodeAddr {
			if _, ok := obs.Health[owner]; !ok {
				obs.Health[owner] = "online"
			}
		}

		if _, exists := obs.Topologies[owner]; !exists {
			peer, err := s.queryClusterFailoverState(owner)
			if err == nil {
				topology, topoErr := clusterShardReplicaTopologyFromPeerState(state, peer)
				if topoErr == nil && topology.Owner == owner {
					obs.Topologies[owner] = topology
					obs.Health[owner] = "online"
				}
			}
		}

		topology := obs.Topologies[owner]
		for _, replica := range topology.Replicas {
			if replica == state.nodeAddr {
				obs.Health[replica] = "online"
				continue
			}
			peer, err := s.queryClusterFailoverState(replica)
			if err != nil {
				continue
			}
			if topology.GroupID != "" && peer.GroupID != topology.GroupID {
				continue
			}
			memberFound := false
			for _, member := range peer.Members {
				if member == replica {
					memberFound = true
					break
				}
			}
			if !memberFound {
				continue
			}
			obs.Health[replica] = "online"
		}
	}

	return obs
}

func clusterObservedNodeHealth(observation clusterShardTopologyObservation, addr string) string {
	if health, ok := observation.Health[addr]; ok {
		return health
	}
	return "unknown"
}


func clusterShardHealthReply(state clusterStateSnapshot, observation clusterShardTopologyObservation) []byte {
	owners := clusterOwnersFromOwners(state.owners)
	shardItems := make([][]byte, 0, len(owners))
	overall := "healthy"

	for _, owner := range owners {
		topology := observation.Topologies[owner]
		ownerHealth := clusterObservedNodeHealth(observation, owner)
		replicasTotal := len(topology.Replicas)
		replicasOnline := 0
		replicasUnknown := 0
		for _, replica := range topology.Replicas {
			switch clusterObservedNodeHealth(observation, replica) {
			case "online":
				replicasOnline++
			default:
				replicasUnknown++
			}
		}

		status := "healthy"
		if ownerHealth != "online" || replicasUnknown > 0 {
			status = "unknown"
			if overall == "healthy" {
				overall = "unknown"
			}
		}

		shardItems = append(shardItems, array(
			formatBulkString([]byte("owner")),
			formatBulkString([]byte(owner)),
			formatBulkString([]byte("owner_health")),
			formatBulkString([]byte(ownerHealth)),
			formatBulkString([]byte("replicas_total")),
			integer(int64(replicasTotal)),
			formatBulkString([]byte("replicas_online")),
			integer(int64(replicasOnline)),
			formatBulkString([]byte("replicas_unknown")),
			integer(int64(replicasUnknown)),
			formatBulkString([]byte("status")),
			formatBulkString([]byte(status)),
		))
	}

	assigned := 0
	for _, owner := range state.owners {
		if owner != "" {
			assigned++
		}
	}
	coverageOK := assigned == clusterSlotCount
	if !coverageOK {
		overall = "fail"
	}

	return array(
		formatBulkString([]byte("status")),
		formatBulkString([]byte(overall)),
		formatBulkString([]byte("coverage_ok")),
		integer(boolToInt64(coverageOK)),
		formatBulkString([]byte("assigned_slots")),
		integer(int64(assigned)),
		formatBulkString([]byte("shards")),
		array(shardItems...),
	)
}

func clusterShardHealthCounters(state clusterStateSnapshot, observation clusterShardTopologyObservation) (shards, replicas, online, unknown int) {
	owners := clusterOwnersFromOwners(state.owners)
	shards = len(owners)
	for _, owner := range owners {
		topology := observation.Topologies[owner]
		for _, replica := range topology.Replicas {
			replicas++
			if clusterObservedNodeHealth(observation, replica) == "online" {
				online++
			} else {
				unknown++
			}
		}
	}
	return
}
