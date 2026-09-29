package server

import (
	"testing"
	"time"

	"snugkv/internal/engine"
)

func TestFailoverLeaseExclusiveUntilExpiry(t *testing.T) {
	s := New(engine.New())
	const lineage = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	now := time.Now()
	prepareVotingReplica(t, s, lineage, 100)

	s.failoverVoteMu.Lock()
	s.failoverTerm = 5
	s.failoverVoteMu.Unlock()

	first := s.requestFailoverLease(now, lineage, 5, "leader-a", time.Second)
	if !first.Granted {
		t.Fatalf("first lease=%+v", first)
	}

	conflict := s.requestFailoverLease(now.Add(100*time.Millisecond), lineage, 5, "leader-b", time.Second)
	if conflict.Granted {
		t.Fatalf("conflicting lease granted before expiry: %+v", conflict)
	}

	afterExpiry := s.requestFailoverLease(now.Add(1100*time.Millisecond), lineage, 5, "leader-b", time.Second)
	if !afterExpiry.Granted {
		t.Fatalf("lease not granted after expiry: %+v", afterExpiry)
	}
}

func TestFailoverLeaseRenewSameLeader(t *testing.T) {
	s := New(engine.New())
	const lineage = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	now := time.Now()
	prepareVotingReplica(t, s, lineage, 100)

	s.failoverVoteMu.Lock()
	s.failoverTerm = 7
	s.failoverVoteMu.Unlock()

	first := s.requestFailoverLease(now, lineage, 7, "leader-a", time.Second)
	if !first.Granted {
		t.Fatalf("first lease=%+v", first)
	}
	renew := s.requestFailoverLease(now.Add(500*time.Millisecond), lineage, 7, "leader-a", time.Second)
	if !renew.Granted || renew.ExpiresMS <= first.ExpiresMS {
		t.Fatalf("renew=%+v first=%+v", renew, first)
	}
}

func TestFailoverLeaseRenewsAfterReplicaReparentToLeader(t *testing.T) {
	s := New(engine.New())
	const lineage = "abababababababababababababababababababab"
	const leaderID = "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
	now := time.Now()
	prepareVotingReplica(t, s, lineage, 100)

	s.failoverVoteMu.Lock()
	s.failoverTerm = 11
	s.failoverVoteMu.Unlock()

	first := s.requestFailoverLease(now, lineage, 11, leaderID, time.Second)
	if !first.Granted {
		t.Fatalf("first lease=%+v", first)
	}

	// After reparenting, replication.masterRunID changes from the failed
	// primary lineage to the elected leader's run ID. The existing same-term,
	// same-holder lease must remain renewable.
	s.replication.mu.Lock()
	s.replication.masterRunID = leaderID
	s.replication.mu.Unlock()

	renew := s.requestFailoverLease(now.Add(500*time.Millisecond), lineage, 11, leaderID, time.Second)
	if !renew.Granted || renew.ExpiresMS <= first.ExpiresMS {
		t.Fatalf("renew after reparent=%+v first=%+v", renew, first)
	}

	wrongLeader := s.requestFailoverLease(now.Add(600*time.Millisecond), lineage, 11, "other-leader", time.Second)
	if wrongLeader.Granted {
		t.Fatalf("different leader renewed reparented lease: %+v", wrongLeader)
	}
}

func TestFailoverLeaseRejectsOldTermAndWrongLineage(t *testing.T) {
	s := New(engine.New())
	const lineage = "cccccccccccccccccccccccccccccccccccccccc"
	now := time.Now()
	prepareVotingReplica(t, s, lineage, 100)

	s.failoverVoteMu.Lock()
	s.failoverTerm = 9
	s.failoverVoteMu.Unlock()

	if reply := s.requestFailoverLease(now, lineage, 8, "leader", time.Second); reply.Granted {
		t.Fatalf("old term lease granted: %+v", reply)
	}
	if reply := s.requestFailoverLease(now, "dddddddddddddddddddddddddddddddddddddddd", 9, "leader", time.Second); reply.Granted {
		t.Fatalf("wrong lineage lease granted: %+v", reply)
	}
}

func TestFailoverLeaseRPC(t *testing.T) {
	peer, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()

	const lineage = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	prepareVotingReplica(t, peer.server, lineage, 100)
	peer.server.failoverVoteMu.Lock()
	peer.server.failoverTerm = 3
	peer.server.failoverVoteMu.Unlock()

	reply, err := queryFailoverLease(
		peer.listener.Addr().String(),
		time.Second,
		"",
		"",
		lineage,
		3,
		"leader-a",
		time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Granted || reply.Term != 3 {
		t.Fatalf("lease RPC=%+v", reply)
	}
}
