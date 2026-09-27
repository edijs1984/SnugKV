package server

import (
	"testing"
	"time"

	"snugkv/internal/engine"
)

func TestAutoFailoverDisabledDoesNotPromote(t *testing.T) {
	s := New(engine.New())
	s.replication.setReplica("127.0.0.1", 6390)

	s.replication.mu.Lock()
	s.replication.masterDownSince = time.Now().Add(-time.Hour)
	s.replication.mu.Unlock()

	if err := s.maintainAutoFailover(time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := s.replication.snapshot().role; got != replicationReplica {
		t.Fatalf("role=%v want replica", got)
	}
}

func TestAutoFailoverPromotesAfterContinuousOutage(t *testing.T) {
	s := New(engine.New())
	s.autoFailoverTimeout = 100 * time.Millisecond
	s.replication.setReplica("127.0.0.1", 6390)

	now := time.Now()
	s.replication.mu.Lock()
	s.replication.masterRunID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	s.replication.masterDownSince = now.Add(-101 * time.Millisecond)
	s.replication.mu.Unlock()

	if err := s.maintainAutoFailover(now); err != nil {
		t.Fatal(err)
	}
	state := s.replication.snapshot()
	if state.role != replicationMaster {
		t.Fatalf("role=%v want master", state.role)
	}
	if state.masterHost != "" || state.masterPort != 0 {
		t.Fatalf("upstream retained after promotion: %s:%d", state.masterHost, state.masterPort)
	}
}

func TestAutoFailoverReconnectResetsOutageWindow(t *testing.T) {
	s := New(engine.New())
	s.autoFailoverTimeout = 100 * time.Millisecond
	s.replication.setReplica("127.0.0.1", 6390)

	now := time.Now()
	s.replication.mu.Lock()
	s.replication.masterDownSince = now.Add(-time.Second)
	s.replication.mu.Unlock()

	s.replication.setReplicaConnected()
	s.replication.setReplicaDisconnected()

	if err := s.maintainAutoFailover(now.Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if got := s.replication.snapshot().role; got != replicationReplica {
		t.Fatalf("role=%v want replica after reconnect reset", got)
	}
}


func TestAutoFailoverDoesNotPromoteUnsyncedReplica(t *testing.T) {
	s := New(engine.New())
	s.autoFailoverTimeout = 100 * time.Millisecond
	s.replication.setReplica("127.0.0.1", 6390)

	now := time.Now()
	s.replication.mu.Lock()
	s.replication.masterDownSince = now.Add(-time.Second)
	s.replication.mu.Unlock()

	if err := s.maintainAutoFailover(now); err != nil {
		t.Fatal(err)
	}
	if got := s.replication.snapshot().role; got != replicationReplica {
		t.Fatalf("role=%v want replica for never-synced node", got)
	}
}
