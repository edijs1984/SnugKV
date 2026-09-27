package server

import (
	"testing"
	"time"

	"snugkv/internal/engine"
)

func TestAutoFailoverPeerModeRequiresElectionAndLeaseQuorum(t *testing.T) {
	const lineage = "9999999999999999999999999999999999999999"
	now := time.Now()

	peerA := newFailoverPeerForRound(t, lineage, 100, 100, 1)
	peerB := newFailoverPeerForRound(t, lineage, 90, 100, 1)

	local := New(engine.New())
	setFailoverReplicaState(local, lineage, 120, 100, now.Add(-time.Second))
	local.failoverPeers = []string{
		peerA.listener.Addr().String(),
		peerB.listener.Addr().String(),
	}
	local.failoverQuorum = 2

	if err := local.maintainAutoFailover(now); err != nil {
		t.Fatal(err)
	}
	if got := local.replication.snapshot().role; got != replicationMaster {
		t.Fatalf("role=%v want master", got)
	}
}

func TestAutoFailoverPeerModeStaysReplicaWithoutQuorum(t *testing.T) {
	const lineage = "8888888888888888888888888888888888888888"
	now := time.Now()

	peerA := newFailoverPeerForRound(t, lineage, 100, 100, 1)

	local := New(engine.New())
	setFailoverReplicaState(local, lineage, 120, 100, now.Add(-time.Second))
	local.failoverPeers = []string{
		peerA.listener.Addr().String(),
		"127.0.0.1:1",
	}
	local.failoverQuorum = 3

	if err := local.maintainAutoFailover(now); err != nil {
		t.Fatal(err)
	}
	if got := local.replication.snapshot().role; got != replicationReplica {
		t.Fatalf("role=%v want replica", got)
	}
}
