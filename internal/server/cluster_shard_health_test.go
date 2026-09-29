package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestClusterShardsReportsReachableReplicaOnline(t *testing.T) {
	masterTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer masterTCP.Close()

	replicaTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer replicaTCP.Close()

	master := masterTCP.listener.Addr().String()
	replica := replicaTCP.listener.Addr().String()

	ranges := map[string]string{"0-16383": master}
	if err := masterTCP.server.configureClusterSlots(true, master, ranges); err != nil {
		t.Fatal(err)
	}
	if err := replicaTCP.server.configureClusterSlots(true, replica, ranges); err != nil {
		t.Fatal(err)
	}

	masterTCP.server.failoverAdvertiseAddr = master
	masterTCP.server.failoverPeers = []string{replica}
	masterTCP.server.failoverGroupID = "shard-a"

	replicaTCP.server.failoverAdvertiseAddr = replica
	replicaTCP.server.failoverPeers = []string{master}
	replicaTCP.server.failoverGroupID = "shard-a"

	reply, err := masterTCP.server.clusterShardsReply()
	if err != nil {
		t.Fatal(err)
	}
	text := string(reply)

	replicaID := clusterNodeID(replica)
	idx := strings.Index(text, replicaID)
	if idx < 0 {
		t.Fatalf("replica missing from SHARDS: %q", text)
	}
	tail := text[idx:]
	if !strings.Contains(tail, "$7\r\nreplica\r\n") {
		t.Fatalf("replica role missing: %q", tail)
	}
	if !strings.Contains(tail, "$6\r\nhealth\r\n$6\r\nonline\r\n") {
		t.Fatalf("reachable replica not online: %q", tail)
	}
}

func TestClusterShardsKeepsUnreachableReplicaHealthUnknown(t *testing.T) {
	s := New(engine.New())
	master := "127.0.0.1:7000"
	replica := "127.0.0.1:65531"

	if err := s.configureClusterSlots(true, master, map[string]string{
		"0-16383": master,
	}); err != nil {
		t.Fatal(err)
	}
	s.failoverAdvertiseAddr = master
	s.failoverPeers = []string{replica}
	s.failoverGroupID = "shard-a"

	reply, err := s.clusterShardsReply()
	if err != nil {
		t.Fatal(err)
	}
	text := string(reply)
	replicaID := clusterNodeID(replica)
	idx := strings.Index(text, replicaID)
	if idx < 0 {
		t.Fatalf("replica missing from SHARDS: %q", text)
	}
	tail := text[idx:]
	if !strings.Contains(tail, "$6\r\nhealth\r\n$7\r\nunknown\r\n") {
		t.Fatalf("unreachable replica health should remain unknown: %q", tail)
	}
}

func TestClusterShardHealthObservationRejectsWrongFailoverGroup(t *testing.T) {
	masterTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer masterTCP.Close()

	replicaTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer replicaTCP.Close()

	master := masterTCP.listener.Addr().String()
	replica := replicaTCP.listener.Addr().String()
	ranges := map[string]string{"0-16383": master}

	if err := masterTCP.server.configureClusterSlots(true, master, ranges); err != nil {
		t.Fatal(err)
	}
	if err := replicaTCP.server.configureClusterSlots(true, replica, ranges); err != nil {
		t.Fatal(err)
	}

	masterTCP.server.failoverAdvertiseAddr = master
	masterTCP.server.failoverPeers = []string{replica}
	masterTCP.server.failoverGroupID = "shard-a"

	replicaTCP.server.failoverAdvertiseAddr = replica
	replicaTCP.server.failoverPeers = []string{master}
	replicaTCP.server.failoverGroupID = "wrong-group"

	obs := masterTCP.server.clusterShardTopologyObservation(masterTCP.server.clusterStateSnapshot())
	if health := clusterObservedNodeHealth(obs, replica); health != "unknown" {
		t.Fatalf("wrong-group replica health=%q want unknown", health)
	}
}
