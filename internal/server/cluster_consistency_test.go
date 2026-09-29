package server

import (
	"strings"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func TestClusterRebalanceExecuteRejectsStaleOwnershipDigest(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	c := "127.0.0.1:7002"

	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-8191":     a,
		"8192-16383": b,
	}); err != nil {
		t.Fatal(err)
	}
	staleDigest := clusterOwnershipDigest(s.clusterStateSnapshot())

	s.clusterMu.Lock()
	s.clusterSlotOwners[9000] = c
	s.clusterKnownNodes[c] = struct{}{}
	s.clusterTopologyEpoch++
	s.clusterMu.Unlock()

	err := s.executeRemoteRebalanceSlot(100, clusterNodeID(b), staleDigest)
	if err == nil || !strings.Contains(err.Error(), "topology fence rejected stale coordinator") {
		t.Fatalf("err=%v", err)
	}
}

func TestClusterFailoverOwnerRejectsStaleOwnershipDigest(t *testing.T) {
	s := New(engine.New())
	oldOwner := "127.0.0.1:7000"
	newOwner := "127.0.0.1:7001"
	other := "127.0.0.1:7002"

	if err := s.configureClusterSlots(true, newOwner, map[string]string{
		"0-8191":     oldOwner,
		"8192-16383": other,
	}); err != nil {
		t.Fatal(err)
	}
	staleDigest := clusterOwnershipDigest(s.clusterStateSnapshot())

	s.clusterMu.Lock()
	s.clusterSlotOwners[12000] = newOwner
	s.clusterTopologyEpoch++
	s.clusterMu.Unlock()

	err := s.replaceClusterOwnerFenced(oldOwner, newOwner, staleDigest)
	if err == nil || !strings.Contains(err.Error(), "ownership fence rejected stale coordinator") {
		t.Fatalf("err=%v", err)
	}

	if got := s.clusterStateSnapshot().owners[0]; got != oldOwner {
		t.Fatalf("stale failover command mutated old-owner slots: %q", got)
	}
}

func TestFailoverForeignLeaseFencesOldPrimaryWrites(t *testing.T) {
	s := New(engine.New())

	s.replication.mu.RLock()
	localID := s.replication.runID
	s.replication.mu.RUnlock()
	if localID == "" {
		t.Fatal("local replication run id is empty")
	}

	s.failoverLeaseMu.Lock()
	s.failoverLeaseTerm = 11
	s.failoverLeaseHolder = "new-leader-id"
	s.failoverLeaseUntil = time.Now().Add(time.Minute)
	s.failoverLeaseMu.Unlock()

	if _, err := s.execute([][]byte{
		[]byte("SET"), []byte("fenced:key"), []byte("blocked"),
	}); err == nil || !strings.HasPrefix(err.Error(), "READONLY ") {
		t.Fatalf("write err=%v want READONLY", err)
	}

	if _, err := s.execute([][]byte{
		[]byte("GET"), []byte("fenced:key"),
	}); err != nil {
		t.Fatalf("read while fenced: %v", err)
	}
}

func TestFailoverExpiredForeignLeaseDoesNotFenceWrites(t *testing.T) {
	s := New(engine.New())

	s.failoverLeaseMu.Lock()
	s.failoverLeaseTerm = 11
	s.failoverLeaseHolder = "old-leader-id"
	s.failoverLeaseUntil = time.Now().Add(-time.Millisecond)
	s.failoverLeaseMu.Unlock()

	if _, err := s.execute([][]byte{
		[]byte("SET"), []byte("lease:expired"), []byte("ok"),
	}); err != nil {
		t.Fatalf("write after foreign lease expiry: %v", err)
	}
}

func TestClusterConsistencyReportsForeignLeaseFence(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-16383": a,
	}); err != nil {
		t.Fatal(err)
	}

	s.failoverLeaseMu.Lock()
	s.failoverLeaseTerm = 4
	s.failoverLeaseHolder = "other-leader"
	s.failoverLeaseUntil = time.Now().Add(time.Minute)
	s.failoverLeaseMu.Unlock()

	got, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("CONSISTENCY"),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, want := range []string{
		"$6\r\nstatus\r\n$8\r\ndegraded\r\n",
		"$13\r\nwrites_fenced\r\n:1\r\n",
		"$12\r\nfence_reason\r\n$20\r\nforeign_leader_lease\r\n",
		"$11\r\ncoverage_ok\r\n:1\r\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("CONSISTENCY missing %q: %q", want, text)
		}
	}
}

func TestClusterConsistencyFailsOnIncompleteCoverage(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-100": a,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("CONSISTENCY"),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, "$6\r\nstatus\r\n$4\r\nfail\r\n") {
		t.Fatalf("CONSISTENCY=%q", text)
	}
	if !strings.Contains(text, "$11\r\ncoverage_ok\r\n:0\r\n") {
		t.Fatalf("CONSISTENCY=%q", text)
	}
}
