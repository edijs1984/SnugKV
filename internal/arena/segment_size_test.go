package arena

import "testing"

func TestSegmentSizeForMediumBlocksAvoidsTailWaste(t *testing.T) {
	tests := []struct {
		payload     int
		wantBlock   int
		wantSegment int
	}{
		// Small allocations keep the shared 8 KiB segment.
		{payload: 356, wantBlock: 384, wantSegment: 8192},

		// Representative packed HASH sizes from the benchmark matrix.
		{payload: 1412, wantBlock: 1536, wantSegment: 7680},
		{payload: 2820, wantBlock: 3072, wantSegment: 6144},
		{payload: 5637, wantBlock: 6144, wantSegment: 6144},
	}

	for _, tt := range tests {
		_, block := class(tt.payload)
		if block != tt.wantBlock {
			t.Fatalf("payload %d block = %d, want %d", tt.payload, block, tt.wantBlock)
		}

		got := segmentSizeForBlock(block)
		if got != tt.wantSegment {
			t.Fatalf("payload %d segment = %d, want %d", tt.payload, got, tt.wantSegment)
		}
		if got%block != 0 {
			t.Fatalf("payload %d segment %d is not an exact multiple of block %d", tt.payload, got, block)
		}
	}
}

func TestMediumBlocksFillAdaptiveSegmentExactly(t *testing.T) {
	var a Arena

	const payload = 2820
	_, block := class(payload)
	if block != 3072 {
		t.Fatalf("block = %d, want 3072", block)
	}

	growth := a.GrowthFor([]int{payload, payload})
	first := a.Alloc(make([]byte, payload))
	second := a.Alloc(make([]byte, payload))

	if len(a.segments) != 1 {
		t.Fatalf("segments = %d, want 1", len(a.segments))
	}
	if got := len(a.segments[0].data); got != 6144 {
		t.Fatalf("segment bytes = %d, want 6144", got)
	}
	if got := int(a.segments[0].used); got != 6144 {
		t.Fatalf("used bytes = %d, want 6144", got)
	}
	if got := a.MemoryBytes(); got != growth {
		t.Fatalf("memory bytes = %d, projected growth %d", got, growth)
	}
	if _, err := a.View(first); err != nil {
		t.Fatal(err)
	}
	if _, err := a.View(second); err != nil {
		t.Fatal(err)
	}

	before := a.MemoryBytes()
	projected := a.GrowthFor([]int{payload})
	third := a.Alloc(make([]byte, payload))
	if got := a.MemoryBytes() - before; got != projected {
		t.Fatalf("growth after full segment = %d, projected %d", got, projected)
	}
	if _, err := a.View(third); err != nil {
		t.Fatal(err)
	}
}
