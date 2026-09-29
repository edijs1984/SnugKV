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

func TestFailoverDemoteRestartedPrimaryWithReparentedReplicaQuorum(t *testing.T) {
	const lineage = "6767676767676767676767676767676767676767"
	const leaderID = "abababababababababababababababababababab"
	now := time.Now()

	leader, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer leader.Close()
	leader.server.replication.mu.Lock()
	leader.server.replication.runID = leaderID
	leader.server.replication.role = replicationMaster
	leader.server.replication.mu.Unlock()
	leader.server.failoverVoteMu.Lock()
	leader.server.failoverTerm = 12
	leader.server.failoverVoteMu.Unlock()
	leader.server.failoverQuorum = 2
	leader.server.activateFailoverLeader(12, lineage, leaderID, now.Add(3*time.Second))

	follower, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer follower.Close()
	follower.server.replication.mu.Lock()
	follower.server.replication.role = replicationReplica
	follower.server.replication.masterRunID = leaderID
	follower.server.replication.masterLinkStatus = "up"
	follower.server.replication.mu.Unlock()
	follower.server.failoverVoteMu.Lock()
	follower.server.failoverTerm = 12
	follower.server.failoverVoteMu.Unlock()
	follower.server.failoverLeaseMu.Lock()
	follower.server.failoverLeaseTerm = 12
	follower.server.failoverLeaseHolder = leaderID
	follower.server.failoverLeaseUntil = now.Add(3 * time.Second)
	follower.server.failoverLeaseMu.Unlock()

	oldPrimary, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer oldPrimary.Close()
	oldAddr := oldPrimary.listener.Addr().String()
	oldPrimary.server.replication.mu.Lock()
	oldPrimary.server.replication.runID = "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
	oldPrimary.server.replication.role = replicationMaster
	oldPrimary.server.replication.mu.Unlock()
	oldPrimary.server.failoverAdvertiseAddr = oldAddr
	oldPrimary.server.failoverQuorum = 2
	oldPrimary.server.failoverPeers = []string{
		leader.listener.Addr().String(),
		follower.listener.Addr().String(),
	}
	oldPrimary.server.activateFailoverLeader(
		1,
		oldPrimary.server.replication.runID,
		oldPrimary.server.replication.runID,
		time.Time{},
	)

	// All three nodes must describe the same failover membership.
	group := "restart-reparent-quorum"
	for _, srv := range []*Server{leader.server, follower.server, oldPrimary.server} {
		srv.failoverGroupID = group
		srv.failoverConfigEpoch = 1
		srv.failoverQuorum = 2
	}
	leader.server.failoverAdvertiseAddr = leader.listener.Addr().String()
	leader.server.failoverPeers = []string{oldAddr, follower.listener.Addr().String()}
	follower.server.failoverAdvertiseAddr = follower.listener.Addr().String()
	follower.server.failoverPeers = []string{oldAddr, leader.listener.Addr().String()}

	reply, err := oldPrimary.server.requestFailoverDemote(
		now,
		lineage,
		12,
		leaderID,
		oldAddr,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Accepted {
		t.Fatalf("demotion rejected with leader + reparented replica quorum: %+v", reply)
	}
	if got := oldPrimary.server.replication.snapshot().role; got != replicationReplica {
		t.Fatalf("role=%v want replica", got)
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
	oldPrimaryAddr := oldPrimary.listener.Addr().String()
	oldPrimary.server.replication.mu.Lock()
	// Simulate a restart: the old primary's process run ID no longer matches
	// the historical failed-primary lineage.
	oldPrimary.server.replication.runID = "dddddddddddddddddddddddddddddddddddddddd"
	oldPrimary.server.replication.role = replicationMaster
	oldPrimary.server.replication.mu.Unlock()
	oldPrimary.server.failoverAdvertiseAddr = oldPrimaryAddr
	oldPrimary.server.failoverQuorum = 2
	oldPrimary.server.failoverPeers = []string{
		leader.listener.Addr().String(),
		voter.listener.Addr().String(),
	}
	oldPrimary.server.activateFailoverLeader(
		1,
		oldPrimary.server.replication.runID,
		oldPrimary.server.replication.runID,
		time.Time{},
	)

	leader.server.failoverPeers = []string{
		oldPrimaryAddr,
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
	t.Fatalf("returning restarted old primary was not demoted: %+v", oldPrimary.server.replication.snapshot())
}
