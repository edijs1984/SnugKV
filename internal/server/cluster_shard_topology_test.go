package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestClusterShardTopologyReportsReplicaUnderOwner(t *testing.T) {
	s := New(engine.New())
	master := "127.0.0.1:7000"
	replica := "127.0.0.1:7001"
	other := "127.0.0.1:7002"

	if err := s.configureClusterSlots(true, replica, map[string]string{
		"0-8191":     master,
		"8192-16383": other,
	}); err != nil {
		t.Fatal(err)
	}
	s.failoverAdvertiseAddr = replica
	s.failoverPeers = []string{master}
	s.failoverGroupID = "shard-a"

	shards, err := s.clusterShardsReply()
	if err != nil {
		t.Fatal(err)
	}
	text := string(shards)
	for _, want := range []string{
		clusterNodeID(master),
		clusterNodeID(replica),
		":7000\r\n",
		":7001\r\n",
		"$6\r\nmaster\r\n",
		"$7\r\nreplica\r\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("CLUSTER SHARDS missing %q: %q", want, text)
		}
	}

	nodes := string(s.clusterNodesReply())
	replicaLine := ""
	for _, line := range strings.Split(nodes, "\n") {
		if strings.Contains(line, replica+"@0") {
			replicaLine = line
			break
		}
	}
	if replicaLine == "" {
		t.Fatalf("CLUSTER NODES missing replica %s: %q", replica, nodes)
	}
	if !strings.Contains(replicaLine, "myself,slave") {
		t.Fatalf("replica line has wrong flags: %q", replicaLine)
	}
	if !strings.Contains(replicaLine, clusterNodeID(master)) {
		t.Fatalf("replica line missing master id %s: %q", clusterNodeID(master), replicaLine)
	}
}

func TestClusterRebalanceExcludesFailoverReplicaFromCapacity(t *testing.T) {
	s := New(engine.New())
	master := "127.0.0.1:7000"
	replica := "127.0.0.1:7001"
	other := "127.0.0.1:7002"

	if err := s.configureClusterSlots(true, replica, map[string]string{
		"0-8191":     master,
		"8192-16383": other,
	}); err != nil {
		t.Fatal(err)
	}
	s.failoverAdvertiseAddr = replica
	s.failoverPeers = []string{master}
	s.failoverGroupID = "shard-a"

	state := s.clusterStateSnapshot()
	capacity := clusterSlotCapableNodesFromState(state)
	if len(capacity) != 2 {
		t.Fatalf("capacity=%v want two masters", capacity)
	}
	for _, node := range capacity {
		if node == replica {
			t.Fatalf("replica incorrectly counted as slot capacity: %v", capacity)
		}
	}

	moves, err := planClusterRebalance(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 0 {
		t.Fatalf("balanced two-master topology planned moves through replica: %+v", moves)
	}
}

func TestClusterShardTopologyFlipsRolesAfterFailoverOwnershipTransfer(t *testing.T) {
	s := New(engine.New())
	oldOwner := "127.0.0.1:7000"
	newOwner := "127.0.0.1:7001"
	other := "127.0.0.1:7002"

	if err := s.configureClusterSlots(true, newOwner, map[string]string{
		"0-8191":     oldOwner,
		"8192-16383": other,
	}); err != nil {
		t.Fatal(err)
	}
	s.failoverAdvertiseAddr = newOwner
	s.failoverPeers = []string{oldOwner}
	s.failoverGroupID = "shard-a"

	before, err := s.localClusterShardReplicaTopology(s.clusterStateSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if before.Owner != oldOwner || len(before.Replicas) != 1 || before.Replicas[0] != newOwner {
		t.Fatalf("before=%+v", before)
	}

	if err := s.replaceClusterOwner(oldOwner, newOwner); err != nil {
		t.Fatal(err)
	}

	afterState := s.clusterStateSnapshot()
	after, err := s.localClusterShardReplicaTopology(afterState)
	if err != nil {
		t.Fatal(err)
	}
	if after.Owner != newOwner || len(after.Replicas) != 1 || after.Replicas[0] != oldOwner {
		t.Fatalf("after=%+v", after)
	}
	if afterState.owners[0] != newOwner || afterState.owners[8191] != newOwner {
		t.Fatalf("promoted owner did not retain shard range")
	}

	nodes := string(s.clusterNodesReply())
	newOwnerLine := ""
	oldOwnerLine := ""
	for _, line := range strings.Split(nodes, "\n") {
		if strings.Contains(line, newOwner+"@0") {
			newOwnerLine = line
		}
		if strings.Contains(line, oldOwner+"@0") {
			oldOwnerLine = line
		}
	}
	if !strings.Contains(newOwnerLine, "myself,master") {
		t.Fatalf("new owner line=%q", newOwnerLine)
	}
	if !strings.Contains(oldOwnerLine, "slave") || !strings.Contains(oldOwnerLine, clusterNodeID(newOwner)) {
		t.Fatalf("old owner line=%q", oldOwnerLine)
	}
}

func TestClusterInfoCountsReplicaTopologyNode(t *testing.T) {
	s := New(engine.New())
	master := "127.0.0.1:7000"
	replica := "127.0.0.1:7001"
	other := "127.0.0.1:7002"

	if err := s.configureClusterSlots(true, replica, map[string]string{
		"0-8191":     master,
		"8192-16383": other,
	}); err != nil {
		t.Fatal(err)
	}
	s.failoverAdvertiseAddr = replica
	s.failoverPeers = []string{master}

	info := string(s.clusterInfoReply())
	if !strings.Contains(info, "cluster_known_nodes:3") {
		t.Fatalf("INFO=%q", info)
	}
	if !strings.Contains(info, "cluster_size:2") {
		t.Fatalf("INFO=%q", info)
	}
}
