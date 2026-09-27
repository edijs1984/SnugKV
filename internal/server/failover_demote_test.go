package server

import (
	"testing"
	"time"

	"snugkv/internal/engine"
)

func TestFailoverDemoteReturningOldPrimaryWithLeaseQuorum(t *testing.T) {
	const lineage = "1212121212121212121212121212121212121212"
	now := time.Now()

	leader, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer leader.Close()
	leader.server.replication.mu.Lock()
	leader.server.replication.runID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	leader.server.replication.mu.Unlock()
	leaderID := leader.server.replication.runID
	leader.server.failoverQuorum = 2
	leader.server.activateFailoverLeader(6, lineage, leaderID, now.Add(3*time.Second))
	leader.server.failoverVoteMu.Lock()
	leader.server.failoverTerm = 6
	leader.server.failoverVoteMu.Unlock()

	voter, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer voter.Close()
	prepareVotingReplica(t, voter.server, lineage, 100)
	voter.server.failoverVoteMu.Lock()
	voter.server.failoverTerm = 6
	voter.server.failoverVoteMu.Unlock()

	oldPrimary, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer oldPrimary.Close()
	oldPrimary.server.replication.mu.Lock()
	oldPrimary.server.replication.runID = lineage
	oldPrimary.server.replication.role = replicationMaster
	oldPrimary.server.replication.mu.Unlock()
	oldPrimary.server.failoverQuorum = 2
	oldPrimary.server.failoverPeers = []string{
		leader.listener.Addr().String(),
		voter.listener.Addr().String(),
	}

	reply, err := oldPrimary.server.requestFailoverDemote(now, lineage, 6, leaderID)
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Accepted {
		t.Fatalf("demotion rejected: %+v", reply)
	}

	state := oldPrimary.server.replication.snapshot()
	if state.role != replicationReplica {
		t.Fatalf("role=%v want replica", state.role)
	}
	if state.masterHost == "" || state.masterPort == 0 {
		t.Fatalf("old primary has no new upstream: %+v", state)
	}
}

func TestFailoverDemoteRejectsWithoutLeaseQuorum(t *testing.T) {
	const lineage = "3434343434343434343434343434343434343434"
	now := time.Now()

	leader, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer leader.Close()
	leader.server.replication.mu.Lock()
	leader.server.replication.runID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	leader.server.replication.mu.Unlock()
	leaderID := leader.server.replication.runID
	leader.server.failoverQuorum = 2
	leader.server.activateFailoverLeader(8, lineage, leaderID, now.Add(3*time.Second))
	leader.server.failoverVoteMu.Lock()
	leader.server.failoverTerm = 8
	leader.server.failoverVoteMu.Unlock()

	oldPrimary := New(engine.New())
	oldPrimary.replication.mu.Lock()
	oldPrimary.replication.runID = lineage
	oldPrimary.replication.role = replicationMaster
	oldPrimary.replication.mu.Unlock()
	oldPrimary.failoverQuorum = 2
	oldPrimary.failoverPeers = []string{
		leader.listener.Addr().String(),
		"127.0.0.1:1",
	}

	reply, err := oldPrimary.requestFailoverDemote(now, lineage, 8, leaderID)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Accepted {
		t.Fatalf("demotion accepted without quorum: %+v", reply)
	}
	if got := oldPrimary.replication.snapshot().role; got != replicationMaster {
		t.Fatalf("role=%v want master", got)
	}
}

func TestFailoverLeaderConvergenceDemotesReturningOldPrimary(t *testing.T) {
	const lineage = "5656565656565656565656565656565656565656"
	now := time.Now()

	leader, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer leader.Close()
	leader.server.replication.mu.Lock()
	leader.server.replication.runID = "cccccccccccccccccccccccccccccccccccccccc"
	leader.server.replication.mu.Unlock()
	leaderID := leader.server.replication.runID
	leader.server.failoverQuorum = 2
	leader.server.activateFailoverLeader(10, lineage, leaderID, now.Add(3*time.Second))
	leader.server.failoverVoteMu.Lock()
	leader.server.failoverTerm = 10
	leader.server.failoverVoteMu.Unlock()

	voter, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer voter.Close()
	prepareVotingReplica(t, voter.server, lineage, 100)
	voter.server.failoverVoteMu.Lock()
	voter.server.failoverTerm = 10
	voter.server.failoverVoteMu.Unlock()

	oldPrimary, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer oldPrimary.Close()
	oldPrimary.server.replication.mu.Lock()
	oldPrimary.server.replication.runID = lineage
	oldPrimary.server.replication.role = replicationMaster
	oldPrimary.server.replication.mu.Unlock()
	oldPrimary.server.failoverQuorum = 2
	oldPrimary.server.failoverPeers = []string{
		leader.listener.Addr().String(),
		voter.listener.Addr().String(),
	}

	leader.server.failoverPeers = []string{
		oldPrimary.listener.Addr().String(),
		voter.listener.Addr().String(),
	}
	leader.server.convergeFailoverReplicas(now)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if oldPrimary.server.replication.snapshot().role == replicationReplica {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("returning old primary was not demoted: %+v", oldPrimary.server.replication.snapshot())
}
