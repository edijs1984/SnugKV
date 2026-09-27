package server

import (
	"testing"
	"time"

	"snugkv/internal/engine"
)

func TestFailoverReparentAcceptsLeasedLeader(t *testing.T) {
	const lineage = "abababababababababababababababababababab"
	now := time.Now()

	leader, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer leader.Close()

	leader.server.replication.promote()
	leaderID := leader.server.replication.runID
	leader.server.activateFailoverLeader(7, lineage, leaderID, now.Add(3*time.Second))

	follower, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer follower.Close()

	prepareVotingReplica(t, follower.server, lineage, 100)
	follower.server.failoverPeers = []string{leader.listener.Addr().String()}
	follower.server.failoverVoteMu.Lock()
	follower.server.failoverTerm = 7
	follower.server.failoverVoteMu.Unlock()
	follower.server.failoverLeaseMu.Lock()
	follower.server.failoverLeaseTerm = 7
	follower.server.failoverLeaseHolder = leaderID
	follower.server.failoverLeaseUntil = now.Add(3 * time.Second)
	follower.server.failoverLeaseMu.Unlock()

	reply, err := follower.server.requestFailoverReparent(now, lineage, 7, leaderID)
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Accepted {
		t.Fatalf("reparent rejected: %+v", reply)
	}

	state := follower.server.replication.snapshot()
	if state.role != replicationReplica {
		t.Fatalf("role=%v want replica", state.role)
	}
	if state.masterPort == 6390 {
		t.Fatalf("upstream was not changed: %+v", state)
	}
}

func TestFailoverReparentRejectsWrongLeaseHolder(t *testing.T) {
	const lineage = "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
	now := time.Now()

	leader, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer leader.Close()

	leader.server.replication.promote()
	leaderID := leader.server.replication.runID
	leader.server.activateFailoverLeader(9, lineage, leaderID, now.Add(3*time.Second))

	follower := New(engine.New())
	prepareVotingReplica(t, follower, lineage, 100)
	follower.failoverPeers = []string{leader.listener.Addr().String()}
	follower.failoverVoteMu.Lock()
	follower.failoverTerm = 9
	follower.failoverVoteMu.Unlock()
	follower.failoverLeaseMu.Lock()
	follower.failoverLeaseTerm = 9
	follower.failoverLeaseHolder = "different-leader"
	follower.failoverLeaseUntil = now.Add(3 * time.Second)
	follower.failoverLeaseMu.Unlock()

	reply, err := follower.requestFailoverReparent(now, lineage, 9, leaderID)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Accepted {
		t.Fatalf("reparent accepted without matching lease: %+v", reply)
	}
	state := follower.replication.snapshot()
	if state.masterHost != "127.0.0.1" || state.masterPort != 6390 {
		t.Fatalf("topology changed after rejected reparent: %+v", state)
	}
}

func TestFailoverReparentRPC(t *testing.T) {
	const lineage = "efefefefefefefefefefefefefefefefefefefef"
	now := time.Now()

	leader, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer leader.Close()
	leader.server.replication.promote()
	leaderID := leader.server.replication.runID
	leader.server.activateFailoverLeader(4, lineage, leaderID, now.Add(3*time.Second))

	follower, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer follower.Close()
	prepareVotingReplica(t, follower.server, lineage, 100)
	follower.server.failoverPeers = []string{leader.listener.Addr().String()}
	follower.server.failoverVoteMu.Lock()
	follower.server.failoverTerm = 4
	follower.server.failoverVoteMu.Unlock()
	follower.server.failoverLeaseMu.Lock()
	follower.server.failoverLeaseTerm = 4
	follower.server.failoverLeaseHolder = leaderID
	follower.server.failoverLeaseUntil = now.Add(3 * time.Second)
	follower.server.failoverLeaseMu.Unlock()

	reply, err := queryFailoverReparent(
		follower.listener.Addr().String(),
		time.Second,
		"",
		"",
		lineage,
		4,
		leaderID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Accepted {
		t.Fatalf("RPC reparent rejected: %+v", reply)
	}
}
