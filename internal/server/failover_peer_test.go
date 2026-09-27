package server

import (
	"net"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func setFailoverReplicaState(s *Server, runID string, offset int64, priority int, downSince time.Time) {
	s.autoFailoverTimeout = 100 * time.Millisecond
	s.failoverPriority = priority
	s.replication.setReplica("127.0.0.1", 6390)
	s.replication.mu.Lock()
	s.replication.masterRunID = runID
	s.replication.offset = offset
	s.replication.masterLinkStatus = "down"
	s.replication.masterSyncInProgress = false
	s.replication.masterDownSince = downSince
	s.replication.mu.Unlock()
}

func TestFailoverPeerStateExchange(t *testing.T) {
	peer, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()

	const runID = "1111111111111111111111111111111111111111"
	now := time.Now()
	setFailoverReplicaState(peer.server, runID, 123, 50, now.Add(-time.Second))

	addr := peer.listener.Addr().(*net.TCPAddr)
	state, err := queryFailoverPeer(addr.String(), time.Second, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if state.NodeID == "" || state.Role != "replica" || !state.MasterDown {
		t.Fatalf("unexpected peer state: %+v", state)
	}
	if state.MasterRunID != runID || state.Offset != 123 || state.Priority != 50 {
		t.Fatalf("unexpected peer metadata: %+v", state)
	}
}

func TestEvaluatePeerFailoverUsesMatchingLineage(t *testing.T) {
	peerA, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer peerA.Close()

	peerB, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer peerB.Close()

	const runID = "2222222222222222222222222222222222222222"
	now := time.Now()
	setFailoverReplicaState(peerA.server, runID, 200, 100, now.Add(-time.Second))
	setFailoverReplicaState(peerB.server, "3333333333333333333333333333333333333333", 999, 1, now.Add(-time.Second))

	local := New(engine.New())
	setFailoverReplicaState(local, runID, 100, 100, now.Add(-time.Second))
	local.failoverPeers = []string{peerA.listener.Addr().String(), peerB.listener.Addr().String()}
	local.failoverQuorum = 2

	result, err := local.evaluatePeerFailover(now)
	if err != nil {
		t.Fatal(err)
	}
	if !result.QuorumReached {
		t.Fatalf("expected quorum from local + matching peer: %+v", result)
	}
	if result.CandidateID != peerA.server.replication.runID {
		t.Fatalf("candidate=%q want peer A node id %q", result.CandidateID, peerA.server.replication.runID)
	}
}

func TestPeerConfiguredAutoFailoverRemainsFailClosed(t *testing.T) {
	s := New(engine.New())
	const runID = "4444444444444444444444444444444444444444"
	now := time.Now()
	setFailoverReplicaState(s, runID, 100, 100, now.Add(-time.Second))
	s.failoverPeers = []string{"127.0.0.1:1"}
	s.failoverQuorum = 2

	if err := s.maintainAutoFailover(now); err != nil {
		t.Fatal(err)
	}
	if got := s.replication.snapshot().role; got != replicationReplica {
		t.Fatalf("peer-configured node promoted before term voting: role=%v", got)
	}
}
