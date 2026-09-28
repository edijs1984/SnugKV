package server

import (
	"errors"
	"sort"
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
