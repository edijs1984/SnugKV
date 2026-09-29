package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestClusterShardTopologyDiscoversRemoteShardReplicas(t *testing.T) {
	aTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer aTCP.Close()

	bTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer bTCP.Close()

	a := aTCP.listener.Addr().String()
	b := bTCP.listener.Addr().String()
	aReplica := "127.0.0.1:7101"
	bReplica := "127.0.0.1:7201"
	ranges := map[string]string{
		"0-8191":     a,
		"8192-16383": b,
	}
	if err := aTCP.server.configureClusterSlots(true, a, ranges); err != nil {
		t.Fatal(err)
	}
	if err := bTCP.server.configureClusterSlots(true, b, ranges); err != nil {
		t.Fatal(err)
	}

	aTCP.server.failoverAdvertiseAddr = a
	aTCP.server.failoverPeers = []string{aReplica}
	aTCP.server.failoverGroupID = "shard-a"

	bTCP.server.failoverAdvertiseAddr = b
	bTCP.server.failoverPeers = []string{bReplica}
	bTCP.server.failoverGroupID = "shard-b"

	shards, err := aTCP.server.clusterShardsReply()
	if err != nil {
		t.Fatal(err)
	}
	text := string(shards)
	for _, want := range []string{
		clusterNodeID(a),
		clusterNodeID(aReplica),
		clusterNodeID(b),
		clusterNodeID(bReplica),
		":7101\r\n",
		":7201\r\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("CLUSTER SHARDS missing %q: %q", want, text)
		}
	}

	nodes := string(aTCP.server.clusterNodesReply())
	for _, replicaCase := range []struct {
		addr   string
		master string
	}{
		{aReplica, a},
		{bReplica, b},
	} {
		line := ""
		for _, candidate := range strings.Split(nodes, "\n") {
			if strings.Contains(candidate, replicaCase.addr+"@0") {
				line = candidate
				break
			}
		}
		if line == "" {
			t.Fatalf("CLUSTER NODES missing replica %s: %q", replicaCase.addr, nodes)
		}
		if !strings.Contains(line, "slave") {
			t.Fatalf("replica %s not marked slave: %q", replicaCase.addr, line)
		}
		if !strings.Contains(line, clusterNodeID(replicaCase.master)) {
			t.Fatalf("replica %s missing master id: %q", replicaCase.addr, line)
		}
	}

	info := string(aTCP.server.clusterInfoReply())
	if !strings.Contains(info, "cluster_known_nodes:4") {
		t.Fatalf("CLUSTER INFO=%q", info)
	}
	if !strings.Contains(info, "cluster_size:2") {
		t.Fatalf("CLUSTER INFO=%q", info)
	}
}

func TestClusterRemoteReplicaMetadataDoesNotAffectRebalanceCapacity(t *testing.T) {
	aTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer aTCP.Close()

	bTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer bTCP.Close()

	a := aTCP.listener.Addr().String()
	b := bTCP.listener.Addr().String()
	bReplica := "127.0.0.1:7201"
	ranges := map[string]string{
		"0-8191":     a,
		"8192-16383": b,
	}
	if err := aTCP.server.configureClusterSlots(true, a, ranges); err != nil {
		t.Fatal(err)
	}
	if err := bTCP.server.configureClusterSlots(true, b, ranges); err != nil {
		t.Fatal(err)
	}
	bTCP.server.failoverAdvertiseAddr = b
	bTCP.server.failoverPeers = []string{bReplica}
	bTCP.server.failoverGroupID = "shard-b"

	state := aTCP.server.clusterStateSnapshot()
	capacity := clusterSlotCapableNodesFromState(state)
	if len(capacity) != 2 {
		t.Fatalf("capacity=%v want two masters", capacity)
	}
	for _, node := range capacity {
		if node == bReplica {
			t.Fatalf("remote replica leaked into capacity: %v", capacity)
		}
	}

	moves, err := planClusterRebalance(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 0 {
		t.Fatalf("balanced master topology unexpectedly planned moves: %+v", moves)
	}

	topologies := aTCP.server.clusterShardReplicaTopologies(state)
	if topology, ok := topologies[b]; !ok || len(topology.Replicas) != 1 || topology.Replicas[0] != bReplica {
		t.Fatalf("remote topology=%+v", topologies[b])
	}
}

func TestClusterShardTopologyFallsBackWhenRemoteFailoverStateUnavailable(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	unreachable := "127.0.0.1:65531"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-8191":     a,
		"8192-16383": unreachable,
	}); err != nil {
		t.Fatal(err)
	}

	shards, err := s.clusterShardsReply()
	if err != nil {
		t.Fatal(err)
	}
	text := string(shards)
	if !strings.Contains(text, clusterNodeID(unreachable)) {
		t.Fatalf("unavailable remote owner disappeared from SHARDS: %q", text)
	}
}
