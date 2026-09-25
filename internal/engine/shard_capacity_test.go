package engine

import "testing"

func TestEntryCapacityUsesSparseRampThenDenseGrowthFloor(t *testing.T) {
	var sh shard

	if got := sh.entryCapacityFor(1); got != 4 {
		t.Fatalf("first capacity = %d, want 4", got)
	}

	for _, tc := range []struct {
		length, capacity, want int
	}{
		{4, 4, 6},
		{6, 6, 8},
		{8, 8, 16},
		{16, 16, 32},
		{32, 32, 64},
		{64, 64, 128},
		{128, 128, 192},
		{192, 192, 288},
		{256, 256, 384},
		{320, 320, 480},
		{400, 400, 600},
	} {
		sh.entries = make([]entryData, tc.length, tc.capacity)
		sh.freeIDs = nil
		if got := sh.entryCapacityFor(1); got != tc.want {
			t.Fatalf("len=%d cap=%d next=%d want=%d", tc.length, tc.capacity, got, tc.want)
		}
	}
}

func TestEntryCapacityUsesFreeSlotsBeforeGrowing(t *testing.T) {
	sh := shard{
		entries: make([]entryData, 8, 8),
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
	if got := sh.entryCapacityFor(5); got != 6 {
		t.Fatalf("five-entry sparse batch capacity = %d, want 6", got)
	}
	if got := sh.entryCapacityFor(7); got != 8 {
		t.Fatalf("seven-entry sparse batch capacity = %d, want 8", got)
	}
	if got := sh.entryCapacityFor(63); got != 64 {
		t.Fatalf("batch capacity = %d, want 64", got)
	}
	if got := sh.entryCapacityFor(65); got != 128 {
		t.Fatalf("batch capacity crossing 64 = %d, want 128", got)
	}
	if got := sh.entryCapacityFor(390); got != 432 {
		t.Fatalf("dense shard capacity = %d, want 432", got)
	}
}

func TestEntryCapacityPreservesSparseFourSlotFloor(t *testing.T) {
	var sh shard
	for i := 0; i < 4; i++ {
		if len(sh.entries) == cap(sh.entries) {
			sh.entries = make([]entryData, len(sh.entries), sh.entryCapacityFor(1))
		}
		sh.entries = append(sh.entries, entryData{})
	}
	if cap(sh.entries) != 4 {
		t.Fatalf("sparse shard capacity = %d, want 4", cap(sh.entries))
	}

	if len(sh.entries) == cap(sh.entries) {
		sh.entries = make([]entryData, len(sh.entries), sh.entryCapacityFor(1))
	}
	sh.entries = append(sh.entries, entryData{})
	if cap(sh.entries) != 6 {
		t.Fatalf("five-entry sparse growth = %d, want 6", cap(sh.entries))
	}

	sh.entries = append(sh.entries, entryData{})
	if cap(sh.entries) != 6 {
		t.Fatalf("six-entry sparse capacity = %d, want 6", cap(sh.entries))
	}

	if len(sh.entries) == cap(sh.entries) {
		sh.entries = make([]entryData, len(sh.entries), sh.entryCapacityFor(1))
	}
	sh.entries = append(sh.entries, entryData{})
	if cap(sh.entries) != 8 {
		t.Fatalf("seven-entry sparse growth = %d, want 8", cap(sh.entries))
	}
}
