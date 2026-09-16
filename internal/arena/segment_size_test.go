package arena

import "testing"

func TestSegmentSizeForMediumBlocksAvoidsTailWaste(t *testing.T) {
	tests := []struct {
		payload     int
		wantBlock   int
		wantSegment int
	}{
		// Small allocations keep the shared 8 KiB segment. They are not
		// expected to divide the segment exactly.
		{payload: 48, wantBlock: 56, wantSegment: 8192},
		{payload: 356, wantBlock: 368, wantSegment: 8192},

		// Representative packed HASH sizes from the benchmark matrix.
		{payload: 1412, wantBlock: 1458, wantSegment: 7290},
		{payload: 2820, wantBlock: 2953, wantSegment: 5906},
		{payload: 5637, wantBlock: 5985, wantSegment: 5985},
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

		// Only medium blocks are intentionally packed into exact-multiple
		// segment sizes. Small blocks continue sharing the normal 8 KiB arena.
		if block > 1024 && got%block != 0 {
			t.Fatalf("payload %d segment %d is not an exact multiple of block %d", tt.payload, got, block)
		}
	}
}

func TestMediumGeometricClassesStayWithinFreelist(t *testing.T) {
	previousBucket := 38
	previousBlock := 1024

	for payload := 1017; payload <= 8184; payload += 17 {
		bucket, block := class(payload)
		if bucket < previousBucket {
			t.Fatalf("payload %d bucket regressed: %d < %d", payload, bucket, previousBucket)
		}
		if block < previousBlock {
			t.Fatalf("payload %d block regressed: %d < %d", payload, block, previousBlock)
		}
		if block < payload+8 {
			t.Fatalf("payload %d block %d is too small", payload, block)
		}
		if bucket >= len(Arena{}.free) {
			t.Fatalf("payload %d bucket %d exceeds freelist", payload, bucket)
		}
		previousBucket = bucket
		previousBlock = block
	}

	bucket, block := class(8184)
	if bucket != 56 || block != 8192 {
		t.Fatalf("8 KiB boundary = bucket %d block %d, want bucket 56 block 8192", bucket, block)
	}

	bucket, block = class(32 << 20)
	if bucket != 127 {
		t.Fatalf("32 MiB class bucket = %d, want 127", bucket)
	}
	if block < (32<<20)+8 {
		t.Fatalf("32 MiB class block = %d, too small", block)
	}
}

func TestSmallHashClassesReduceSlack(t *testing.T) {
	tests := []struct {
		payload   int
		wantBlock int
	}{
		{payload: 48, wantBlock: 56},
		{payload: 356, wantBlock: 368},
	}

	for _, tt := range tests {
		bucket, block := class(tt.payload)
		if bucket < 0 || bucket >= len(Arena{}.free) {
			t.Fatalf("payload %d bucket %d outside freelist", tt.payload, bucket)
		}
		if block != tt.wantBlock {
			t.Fatalf("payload %d block = %d, want %d", tt.payload, block, tt.wantBlock)
		}
	}
}

func TestMediumBlocksFillAdaptiveSegmentExactly(t *testing.T) {
	var a Arena

	const payload = 2820
	_, block := class(payload)
	if block != 2953 {
		t.Fatalf("block = %d, want 2953", block)
	}

	growth := a.GrowthFor([]int{payload, payload})
	first := a.Alloc(make([]byte, payload))
	second := a.Alloc(make([]byte, payload))

	if len(a.segments) != 1 {
		t.Fatalf("segments = %d, want 1", len(a.segments))
	}
	if got := cap(a.segments[0].data); got != 5906 {
		t.Fatalf("segment bytes = %d, want 5906", got)
	}
	if got := len(a.segments[0].data); got != 5906 {
		t.Fatalf("used bytes = %d, want 5906", got)
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
