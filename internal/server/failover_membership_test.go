package server

import (
	"testing"
	"time"

	"snugkv/internal/engine"
)

func TestFailoverPeerStateIncludesMembershipIdentity(t *testing.T) {
	s := New(engine.New())
	s.failoverGroupID = "cluster-a"
	s.failoverConfigEpoch = 7

	state := s.localFailoverState(time.Now())
	if state.GroupID != "cluster-a" || state.ConfigEpoch != 7 {
		t.Fatalf("membership=%q/%d", state.GroupID, state.ConfigEpoch)
	}
}

func TestEvaluatePeerFailoverIgnoresMembershipMismatch(t *testing.T) {
	const lineage = "abababababababababababababababababababab"
	now := time.Now()

	peer, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	setFailoverReplicaState(peer.server, lineage, 200, 100, now.Add(-time.Second))
	peer.server.failoverGroupID = "cluster-a"
	peer.server.failoverConfigEpoch = 2

	local := New(engine.New())
	setFailoverReplicaState(local, lineage, 100, 100, now.Add(-time.Second))
	local.failoverGroupID = "cluster-a"
	local.failoverConfigEpoch = 1
	local.failoverPeers = []string{peer.listener.Addr().String()}
	local.failoverQuorum = 2

	result, err := local.evaluatePeerFailover(now)
	if err != nil {
		t.Fatal(err)
	}
	if result.QuorumReached {
		t.Fatalf("mismatched membership counted toward quorum: %+v", result)
	}
}

func TestResolveFailoverLeaderRejectsMembershipMismatch(t *testing.T) {
	leader, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer leader.Close()
	leader.server.replication.promote()
	leader.server.failoverGroupID = "cluster-a"
	leader.server.failoverConfigEpoch = 4

	local := New(engine.New())
	local.failoverGroupID = "cluster-a"
	local.failoverConfigEpoch = 3
	local.failoverPeers = []string{leader.listener.Addr().String()}

	if _, err := local.resolveFailoverLeaderAddr(leader.server.replication.runID); err == nil {
		t.Fatal("resolved leader from mismatched membership epoch")
	}
}
