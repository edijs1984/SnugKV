package server

import (
	"path/filepath"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func attachFailoverMembershipPersistence(t *testing.T, s *Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "replication-state")
	replicationPersistencePaths.Store(s, path)
	t.Cleanup(func() { replicationPersistencePaths.Delete(s) })
	return path
}

func TestFailoverMembershipPreparePersistsAndRecovers(t *testing.T) {
	s := New(engine.New())
	s.failoverGroupID = "cluster-a"
	s.failoverConfigEpoch = 3
	s.failoverPeers = []string{"127.0.0.1:7001", "127.0.0.1:7002"}
	s.failoverQuorum = 2
	path := attachFailoverMembershipPersistence(t, s)

	reply, err := s.prepareFailoverMembership(
		"cluster-a",
		3,
		4,
		[]string{"127.0.0.1:7101", "127.0.0.1:7102"},
		2,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Accepted || !reply.Joint || reply.PendingEpoch != 4 {
		t.Fatalf("prepare reply=%+v", reply)
	}

	recovered := New(engine.New())
	if err := recovered.loadFailoverMembershipState(path); err != nil {
		t.Fatal(err)
	}
	state := recovered.failoverMembershipSnapshot()
	if !state.JointActive || state.ConfigEpoch != 3 || state.PendingEpoch != 4 {
		t.Fatalf("recovered membership=%+v", state)
	}
	if len(state.PendingPeers) != 2 || state.PendingQuorum != 2 {
		t.Fatalf("recovered pending membership=%+v", state)
	}
}

func TestFailoverMembershipCommitInstallsPendingSet(t *testing.T) {
	s := New(engine.New())
	s.failoverGroupID = "cluster-a"
	s.failoverConfigEpoch = 1
	s.failoverPeers = []string{"127.0.0.1:7001", "127.0.0.1:7002"}
	s.failoverQuorum = 2
	attachFailoverMembershipPersistence(t, s)

	newPeers := []string{"127.0.0.1:7101", "127.0.0.1:7102", "127.0.0.1:7103"}
	if _, err := s.prepareFailoverMembership("cluster-a", 1, 2, newPeers, 3); err != nil {
		t.Fatal(err)
	}
	reply, err := s.commitFailoverMembership("cluster-a", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Accepted || reply.Joint || reply.ConfigEpoch != 2 {
		t.Fatalf("commit reply=%+v", reply)
	}

	state := s.failoverMembershipSnapshot()
	if state.JointActive || state.ConfigEpoch != 2 || state.Quorum != 3 {
		t.Fatalf("committed membership=%+v", state)
	}
	if !stringSlicesEqual(state.Peers, newPeers) {
		t.Fatalf("peers=%v want=%v", state.Peers, newPeers)
	}
}

func TestFailoverMembershipAbortKeepsCurrentSet(t *testing.T) {
	s := New(engine.New())
	s.failoverGroupID = "cluster-a"
	s.failoverConfigEpoch = 5
	oldPeers := []string{"127.0.0.1:7001", "127.0.0.1:7002"}
	s.failoverPeers = append([]string(nil), oldPeers...)
	s.failoverQuorum = 2
	attachFailoverMembershipPersistence(t, s)

	if _, err := s.prepareFailoverMembership(
		"cluster-a",
		5,
		6,
		[]string{"127.0.0.1:7201", "127.0.0.1:7202"},
		2,
	); err != nil {
		t.Fatal(err)
	}
	reply, err := s.abortFailoverMembership("cluster-a", 6)
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Accepted || reply.Joint {
		t.Fatalf("abort reply=%+v", reply)
	}
	state := s.failoverMembershipSnapshot()
	if state.ConfigEpoch != 5 || state.JointActive || !stringSlicesEqual(state.Peers, oldPeers) {
		t.Fatalf("membership after abort=%+v", state)
	}
}

func TestAutoFailoverElectionFrozenDuringMembershipTransition(t *testing.T) {
	s := New(engine.New())
	s.autoFailoverTimeout = 100 * time.Millisecond
	s.replication.setReplica("127.0.0.1", 6390)

	now := time.Now()
	s.replication.mu.Lock()
	s.replication.masterRunID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	s.replication.masterDownSince = now.Add(-time.Second)
	s.replication.mu.Unlock()

	s.failoverMembershipMu.Lock()
	s.failoverJointActive = true
	s.failoverMembershipMu.Unlock()

	if err := s.maintainAutoFailover(now); err != nil {
		t.Fatal(err)
	}
	if got := s.replication.snapshot().role; got != replicationReplica {
		t.Fatalf("role=%v want replica while membership transition is active", got)
	}
}

func TestFailoverMembershipPrepareRequiresPersistence(t *testing.T) {
	s := New(engine.New())
	s.failoverGroupID = "cluster-a"
	s.failoverConfigEpoch = 1

	if _, err := s.prepareFailoverMembership(
		"cluster-a",
		1,
		2,
		[]string{"127.0.0.1:7101", "127.0.0.1:7102"},
		2,
	); err == nil {
		t.Fatal("expected membership prepare without persistence to fail")
	}
}
