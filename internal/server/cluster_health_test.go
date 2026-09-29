package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestClusterHealthSummarizesReplicaState(t *testing.T) {
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

	got, err := masterTCP.server.execute([][]byte{
		[]byte("CLUSTER"), []byte("HEALTH"),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, want := range []string{
		"$6\r\nstatus\r\n$7\r\nhealthy\r\n",
		"$11\r\ncoverage_ok\r\n:1\r\n",
		"$14\r\nreplicas_total\r\n:1\r\n",
		"$15\r\nreplicas_online\r\n:1\r\n",
		"$16\r\nreplicas_unknown\r\n:0\r\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("CLUSTER HEALTH missing %q: %q", want, text)
		}
	}
}

func TestClusterHealthUnknownForUnreachableReplica(t *testing.T) {
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

	state := s.clusterStateSnapshot()
	reply := clusterShardHealthReply(state, s.clusterShardTopologyObservation(state))
	text := string(reply)
	if !strings.Contains(text, "$6\r\nstatus\r\n$7\r\nunknown\r\n") {
		t.Fatalf("health=%q", text)
	}
	if !strings.Contains(text, "$16\r\nreplicas_unknown\r\n:1\r\n") {
		t.Fatalf("health=%q", text)
	}
}

func TestClusterHealthFailsOnIncompleteCoverage(t *testing.T) {
	s := New(engine.New())
	master := "127.0.0.1:7000"

	if err := s.configureClusterSlots(true, master, map[string]string{
		"0-100": master,
	}); err != nil {
		t.Fatal(err)
	}

	state := s.clusterStateSnapshot()
	reply := clusterShardHealthReply(state, s.clusterShardTopologyObservation(state))
	text := string(reply)
	if !strings.Contains(text, "$6\r\nstatus\r\n$4\r\nfail\r\n") {
		t.Fatalf("health=%q", text)
	}
	if !strings.Contains(text, "$11\r\ncoverage_ok\r\n:0\r\n") {
		t.Fatalf("health=%q", text)
	}
}

func TestClusterInfoIncludesShardHealthCounters(t *testing.T) {
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

	info := string(masterTCP.server.clusterInfoReply())
	for _, want := range []string{
		"cluster_shards_total:1",
		"cluster_replicas_total:1",
		"cluster_replicas_online:1",
		"cluster_replicas_unknown:0",
	} {
		if !strings.Contains(info, want) {
			t.Fatalf("INFO missing %q: %q", want, info)
		}
	}
}
