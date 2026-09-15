package arena

import (
	"testing"
	"unsafe"
)

func TestFirstSmallSegmentUsesCompactExactMultiple(t *testing.T) {
	for _, payload := range []int{8, 16, 48, 80, 128, 356, 900} {
		_, block := class(payload)
		got := segmentSizeForAllocation(block, 0)
		limit := firstSmallSegmentBytes
		if block > limit {
			limit = block
		}
		if got > limit {
			t.Fatalf("payload %d first segment = %d, exceeds compact limit %d", payload, got, limit)
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
	if len(a.segments[0].data) >= secondSmallSegmentBytes {
		t.Fatalf("first sparse segment = %d, expected smaller than %d", len(a.segments[0].data), secondSmallSegmentBytes)
	}
	if _, err := a.View(ref); err != nil {
		t.Fatal(err)
	}
}

func TestSmallArenaUsesStagedGrowthBeforeDenseSegments(t *testing.T) {
	var a Arena
	payload := make([]byte, 16)
	_, block := class(len(payload))

	firstSize := segmentSizeForAllocation(block, 0)
	firstSlots := firstSize / block
	for i := 0; i < firstSlots; i++ {
		a.Alloc(payload)
	}
	if a.SegmentCount() != 1 {
		t.Fatalf("segments before first overflow = %d, want 1", a.SegmentCount())
	}
	if got := len(a.segments[0].data); got != firstSize {
		t.Fatalf("first segment = %d, want %d", got, firstSize)
	}

	before := a.MemoryBytes()
	projected := a.GrowthFor([]int{len(payload)})
	a.Alloc(payload)
	if a.SegmentCount() != 2 {
		t.Fatalf("segments after first overflow = %d, want 2", a.SegmentCount())
	}
	secondSize := segmentSizeForAllocation(block, 1)
	if got := len(a.segments[1].data); got != secondSize {
		t.Fatalf("second segment = %d, want %d", got, secondSize)
	}
	if got := a.MemoryBytes() - before; got != projected {
		t.Fatalf("second segment growth = %d, projected = %d", got, projected)
	}

	secondSlots := secondSize / block
	for i := 1; i < secondSlots; i++ {
		a.Alloc(payload)
	}
	if a.SegmentCount() != 2 {
		t.Fatalf("segments before second overflow = %d, want 2", a.SegmentCount())
	}

	before = a.MemoryBytes()
	projected = a.GrowthFor([]int{len(payload)})
	a.Alloc(payload)
	if a.SegmentCount() != 3 {
		t.Fatalf("segments after second overflow = %d, want 3", a.SegmentCount())
	}
	denseSize := segmentSizeForBlock(block)
	if got := len(a.segments[2].data); got != denseSize {
		t.Fatalf("third segment = %d, want dense segment %d", got, denseSize)
	}
	if got := a.MemoryBytes() - before; got != projected {
		t.Fatalf("third segment growth = %d, projected = %d", got, projected)
	}
}

func TestArenaKeepsFreeTableLazy(t *testing.T) {
	var a Arena
	if got := unsafe.Sizeof(a); got > 64 {
		t.Fatalf("Arena struct = %d bytes, expected compact lazy-free layout", got)
	}

	ref := a.Alloc(make([]byte, 16))
	if a.free != nil {
		t.Fatal("allocation eagerly created free table")
	}

	a.Free(ref)
	if a.free == nil {
		t.Fatal("first free did not create free table")
	}
	bucket, _ := class(16)
	if a.free[bucket] == 0 {
		t.Fatal("freed block was not linked into free table")
	}

	before := *a.free
	if projected := a.GrowthFor([]int{16}); projected != 0 {
		t.Fatalf("freelist reuse projected growth = %d", projected)
	}
	if *a.free != before {
		t.Fatal("GrowthFor mutated live free table")
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
