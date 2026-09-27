package server

import (
	"strings"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func TestFailoverLeaderRenewsMajorityLeaseAndFencesOnLoss(t *testing.T) {
	const lineage = "7777777777777777777777777777777777777777"
	now := time.Now()

	peerA := newFailoverPeerForRound(t, lineage, 100, 100, 1)
	peerB := newFailoverPeerForRound(t, lineage, 90, 100, 1)

	local := New(engine.New())
	setFailoverReplicaState(local, lineage, 120, 100, now.Add(-time.Second))
	local.failoverPeers = []string{
		peerA.listener.Addr().String(),
		peerB.listener.Addr().String(),
	}
	local.failoverQuorum = 2

	if err := local.maintainAutoFailover(now); err != nil {
		t.Fatal(err)
	}
	if got := local.replication.snapshot().role; got != replicationMaster {
		t.Fatalf("role=%v want master", got)
	}
	active, _, _, _, firstExpiry, fenced := local.failoverLeaderState()
	if !active || fenced || firstExpiry.IsZero() {
		t.Fatalf("invalid leader state: active=%v expiry=%v fenced=%v", active, firstExpiry, fenced)
	}

	if _, err := local.execute([][]byte{[]byte("SET"), []byte("lease:key"), []byte("ok")}); err != nil {
		t.Fatalf("write under valid lease: %v", err)
	}

	renewAt := firstExpiry.Add(-500 * time.Millisecond)
	if err := local.maintainAutoFailover(renewAt); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, renewedExpiry, fenced := local.failoverLeaderState()
	if fenced {
		t.Fatal("leader fenced despite successful renewal")
	}
	if !renewedExpiry.After(firstExpiry) {
		t.Fatalf("lease did not extend: first=%v renewed=%v", firstExpiry, renewedExpiry)
	}

	_ = peerA.Close()
	_ = peerB.Close()

	afterExpiry := renewedExpiry.Add(time.Millisecond)
	if err := local.maintainAutoFailover(afterExpiry); err != nil {
		t.Fatal(err)
	}
	if !local.failoverWritesFenced(afterExpiry) {
		t.Fatal("leader remained writable after losing lease quorum")
	}

	if _, err := local.execute([][]byte{[]byte("SET"), []byte("lease:key"), []byte("blocked")}); err == nil ||
		!strings.HasPrefix(err.Error(), "READONLY ") {
		t.Fatalf("write error=%v want READONLY", err)
	}
	value, err := local.execute([][]byte{[]byte("GET"), []byte("lease:key")})
	if err != nil {
		t.Fatalf("read while fenced: %v", err)
	}
	if string(value) != "$2\r\nok\r\n" {
		t.Fatalf("GET reply=%q", value)
	}
}
