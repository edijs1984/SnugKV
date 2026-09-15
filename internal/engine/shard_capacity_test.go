package engine

import "testing"

func TestEntryCapacityRampsSmallShardsBeforeQuarterGrowth(t *testing.T) {
	var sh shard

	if got := sh.entryCapacityFor(1); got != 8 {
		t.Fatalf("first capacity = %d, want 8", got)
	}

	for _, tc := range []struct {
		length, capacity, want int
	}{
		{8, 8, 16},
		{16, 16, 32},
		{32, 32, 64},
		{64, 64, 80},
		{80, 80, 100},
		{100, 100, 125},
	} {
		sh.entries = make([]entry, tc.length, tc.capacity)
		sh.freeIDs = nil
		if got := sh.entryCapacityFor(1); got != tc.want {
			t.Fatalf("len=%d cap=%d next=%d want=%d", tc.length, tc.capacity, got, tc.want)
		}
	}
}

func TestEntryCapacityUsesFreeSlotsBeforeGrowing(t *testing.T) {
	sh := shard{
		entries: make([]entry, 8, 8),
		freeIDs: []uint32{1, 3, 5},
	}
	if got := sh.entryCapacityFor(3); got != 8 {
		t.Fatalf("capacity grew despite reusable slots: %d", got)
	}
	if got := sh.entryCapacityFor(4); got != 16 {
		t.Fatalf("capacity for one net-new slot = %d, want 16", got)
	}
}

func TestEntryCapacityCanPlanBatchGrowth(t *testing.T) {
	var sh shard
	if got := sh.entryCapacityFor(63); got != 64 {
		t.Fatalf("batch capacity = %d, want 64", got)
	}
	if got := sh.entryCapacityFor(65); got != 80 {
		t.Fatalf("batch capacity crossing 64 = %d, want 80", got)
	}
}
