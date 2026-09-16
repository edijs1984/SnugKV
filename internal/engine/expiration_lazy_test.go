package engine

import "testing"

func TestPersistentScheduleKeepsPositionsNil(t *testing.T) {
	var sh shard

	sh.schedule("persistent", 0)

	if sh.expiration.positions != nil {
		t.Fatal("persistent schedule allocated expiration positions map")
	}
	if sh.expiration.Len() != 0 {
		t.Fatalf("persistent schedule queued %d expirations", sh.expiration.Len())
	}
}

func TestTTLScheduleAllocatesPositionsAndPersistentClearsIt(t *testing.T) {
	var sh shard

	sh.schedule("ttl", stamp(1234))
	if sh.expiration.positions == nil {
		t.Fatal("TTL schedule did not allocate expiration positions map")
	}
	if sh.expiration.Len() != 1 {
		t.Fatalf("TTL schedule queued %d expirations, want 1", sh.expiration.Len())
	}
	if _, ok := sh.expiration.positions["ttl"]; !ok {
		t.Fatal("TTL key missing from expiration positions map")
	}

	sh.schedule("ttl", 0)
	if sh.expiration.Len() != 0 {
		t.Fatalf("persistent reschedule left %d expirations, want 0", sh.expiration.Len())
	}
	if _, ok := sh.expiration.positions["ttl"]; ok {
		t.Fatal("persistent reschedule left TTL key in positions map")
	}
}

func TestPersistentScheduleDoesNotAllocateAfterEmptyQueue(t *testing.T) {
	var sh shard

	sh.schedule("missing", 0)
	sh.schedule("missing-again", 0)

	if sh.expiration.positions != nil {
		t.Fatal("persistent-only scheduling allocated expiration positions map")
	}
}
