package server

import (
	"strings"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func newPrimaryLeaseReplica(t *testing.T, lineage string) *TCPServer {
	t.Helper()
	peer, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	setFailoverReplicaState(peer.server, lineage, 0, 100, time.Now())
	return peer
}

func TestPrimaryAcquiresQuorumLeaseAndRemainsWritable(t *testing.T) {
	now := time.Now()
	primary := New(engine.New())

	primary.replication.mu.RLock()
	lineage := primary.replication.runID
	primary.replication.mu.RUnlock()

	peerA := newPrimaryLeaseReplica(t, lineage)
	peerB := newPrimaryLeaseReplica(t, lineage)

	primary.failoverPeers = []string{
		peerA.listener.Addr().String(),
		peerB.listener.Addr().String(),
	}
	primary.failoverQuorum = 2

	if err := primary.maintainAutoFailover(now); err != nil {
		t.Fatal(err)
	}

	active, _, gotLineage, leaderID, expiresAt, fenced := primary.failoverLeaderState()
	if !active || fenced || expiresAt.IsZero() {
		t.Fatalf("primary lease state active=%v fenced=%v expires=%v", active, fenced, expiresAt)
	}
	if gotLineage != lineage || leaderID != lineage {
		t.Fatalf("lineage=%q leader=%q want=%q", gotLineage, leaderID, lineage)
	}
	if _, err := primary.execute([][]byte{
		[]byte("SET"), []byte("primary:lease"), []byte("ok"),
	}); err != nil {
		t.Fatalf("write under primary quorum lease: %v", err)
	}
}

func TestPrimaryFencesWritesAfterQuorumLeaseLoss(t *testing.T) {
	now := time.Now()
	primary := New(engine.New())

	primary.replication.mu.RLock()
	lineage := primary.replication.runID
	primary.replication.mu.RUnlock()

	peerA := newPrimaryLeaseReplica(t, lineage)
	peerB := newPrimaryLeaseReplica(t, lineage)
	primary.failoverPeers = []string{
		peerA.listener.Addr().String(),
		peerB.listener.Addr().String(),
	}
	primary.failoverQuorum = 2

	if err := primary.maintainAutoFailover(now); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, expiresAt, fenced := primary.failoverLeaderState()
	if fenced || expiresAt.IsZero() {
		t.Fatalf("initial lease fenced=%v expires=%v", fenced, expiresAt)
	}

	_ = peerA.Close()
	_ = peerB.Close()

	if err := primary.maintainAutoFailover(expiresAt.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if !primary.failoverWritesFenced(expiresAt.Add(time.Millisecond)) {
		t.Fatal("isolated primary remained writable after quorum lease expiry")
	}
	if _, err := primary.execute([][]byte{
		[]byte("SET"), []byte("primary:isolated"), []byte("blocked"),
	}); err == nil || !strings.HasPrefix(err.Error(), "READONLY ") {
		t.Fatalf("write err=%v want READONLY", err)
	}
}

func TestReplicaCannotAcquireConflictingLeaseBeforePrimaryLeaseExpiry(t *testing.T) {
	now := time.Now()
	primary := New(engine.New())

	primary.replication.mu.RLock()
	lineage := primary.replication.runID
	primary.replication.mu.RUnlock()

	peer := newPrimaryLeaseReplica(t, lineage)
	primary.failoverPeers = []string{peer.listener.Addr().String()}
	primary.failoverQuorum = 2

	// With quorum=2, self + peer is a majority lease.
	if err := primary.maintainAutoFailover(now); err != nil {
		t.Fatal(err)
	}
	active, term, _, leaderID, expiresAt, fenced := primary.failoverLeaderState()
	if !active || fenced {
		t.Fatalf("primary leader active=%v fenced=%v", active, fenced)
	}

	conflict := peer.server.requestFailoverLease(
		now.Add(100*time.Millisecond),
		lineage,
		term,
		"new-leader",
		time.Second,
	)
	if conflict.Granted {
		t.Fatalf("conflicting leader lease granted while primary lease valid: %+v", conflict)
	}

	after := peer.server.requestFailoverLease(
		expiresAt.Add(time.Second),
		lineage,
		term,
		"new-leader",
		time.Second,
	)
	if !after.Granted {
		t.Fatalf("new leader lease not granted after primary lease expiry: %+v leader=%s", after, leaderID)
	}
}

func TestStandaloneMasterWithoutFailoverQuorumIsNotFenced(t *testing.T) {
	s := New(engine.New())
	if err := s.maintainAutoFailover(time.Now()); err != nil {
		t.Fatal(err)
	}
	if s.failoverWritesFenced(time.Now()) {
		t.Fatal("standalone master unexpectedly fenced")
	}
	if _, err := s.execute([][]byte{
		[]byte("SET"), []byte("standalone"), []byte("ok"),
	}); err != nil {
		t.Fatalf("standalone write: %v", err)
	}
}
