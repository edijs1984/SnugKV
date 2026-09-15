package arena

import "testing"

func TestFirstSmallSegmentUsesCompactExactMultiple(t *testing.T) {
	for _, payload := range []int{8, 16, 48, 80, 128, 356, 900} {
		_, block := class(payload)
		got := segmentSizeForAllocation(block, 0)
		if got > firstSmallSegmentBytes {
			t.Fatalf("payload %d first segment = %d, exceeds %d", payload, got, firstSmallSegmentBytes)
		}
		if got < block || got%block != 0 {
			t.Fatalf("payload %d first segment %d is not an exact block multiple of %d", payload, got, block)
		}
	}
}

func TestFirstArenaGrowthProjectionMatchesAllocation(t *testing.T) {
	var a Arena
	payload := make([]byte, 16)

	projected := a.GrowthFor([]int{len(payload)})
	ref := a.Alloc(payload)
	if got := a.MemoryBytes(); got != projected {
		t.Fatalf("memory = %d, projected = %d", got, projected)
	}
	if a.SegmentCount() != 1 {
		t.Fatalf("segments = %d, want 1", a.SegmentCount())
	}
	if len(a.segments[0].data) >= SegmentBytes {
		t.Fatalf("first sparse segment = %d, expected smaller than %d", len(a.segments[0].data), SegmentBytes)
	}
	if _, err := a.View(ref); err != nil {
		t.Fatal(err)
	}
}

func TestSmallArenaFallsBackToNormalSegmentAfterFirstFills(t *testing.T) {
	var a Arena
	payload := make([]byte, 16)
	_, block := class(len(payload))
	firstSize := segmentSizeForAllocation(block, 0)
	firstSlots := firstSize / block

	for i := 0; i < firstSlots; i++ {
		a.Alloc(payload)
	}
	if a.SegmentCount() != 1 {
		t.Fatalf("segments before overflow = %d, want 1", a.SegmentCount())
	}

	before := a.MemoryBytes()
	projected := a.GrowthFor([]int{len(payload)})
	a.Alloc(payload)
	if a.SegmentCount() != 2 {
		t.Fatalf("segments after overflow = %d, want 2", a.SegmentCount())
	}
	if got := len(a.segments[1].data); got != SegmentBytes {
		t.Fatalf("second segment = %d, want %d", got, SegmentBytes)
	}
	if got := a.MemoryBytes() - before; got != projected {
		t.Fatalf("second segment growth = %d, projected = %d", got, projected)
	}
}

func TestFreelistReuseDoesNotGrowCompactArena(t *testing.T) {
	var a Arena
	ref := a.Alloc(make([]byte, 16))
	before := a.MemoryBytes()
	a.Free(ref)
	if projected := a.GrowthFor([]int{16}); projected != 0 {
		t.Fatalf("freelist reuse projected growth = %d", projected)
	}
	a.Alloc(make([]byte, 16))
	if got := a.MemoryBytes(); got != before {
		t.Fatalf("freelist reuse changed memory: before=%d after=%d", before, got)
	}
}
