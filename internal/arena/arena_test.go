package arena

import (
	"bytes"
	"fmt"
	"testing"
	"unsafe"
)

func TestReuseAndGeneration(t *testing.T) {
	var a Arena
	before := a.MemoryBytes()
	growth := a.GrowthFor([]int{5})
	ref := a.Alloc([]byte("hello"))
	if a.MemoryBytes() != before+growth {
		t.Fatal("growth accounting")
	}
	got, err := a.View(ref)
	if err != nil || string(got) != "hello" {
		t.Fatal(err)
	}
	a.Free(ref)
	if _, err = a.View(ref); err == nil {
		t.Fatal("freed reference accepted")
	}
	before = a.MemoryBytes()
	next := a.Alloc([]byte("world"))
	if a.MemoryBytes() != before {
		t.Fatal("free block not reused")
	}
	if _, err = a.View(ref); err == nil {
		t.Fatal("old generation accepted")
	}
	if got, _ = a.View(next); string(got) != "world" {
		t.Fatal("reused value")
	}
}

func TestBatchProjection(t *testing.T) {
	var a Arena
	lengths := []int{0, 1, 100, 65000, 65536, 130000, 10}
	growth := a.GrowthFor(lengths)
	for _, n := range lengths {
		value := bytes.Repeat([]byte{42}, n)
		ref := a.Alloc(value)
		got, err := a.View(ref)
		if err != nil || !bytes.Equal(value, got) {
			t.Fatal("roundtrip")
		}
	}
	if a.MemoryBytes() != growth {
		t.Fatalf("got %d want %d", a.MemoryBytes(), growth)
	}
}

func TestSegmentDescriptorIs24Bytes(t *testing.T) {
	if got := unsafe.Sizeof(segment{}); got != 24 {
		t.Fatalf("segment size = %d, want 24", got)
	}
	if segmentMetadata != int(unsafe.Sizeof(segment{})) {
		t.Fatalf("segment metadata = %d, struct size = %d", segmentMetadata, unsafe.Sizeof(segment{}))
	}
}

func TestTinySetSizeClasses(t *testing.T) {
	var a Arena

	single := a.Alloc(bytes.Repeat([]byte{'s'}, 16))
	if got := a.AllocationBytes(single); got != 24 {
		t.Fatalf("16-byte payload allocation=%d want 24", got)
	}

	prefix := a.Alloc(bytes.Repeat([]byte{'p'}, 77))
	if got := a.AllocationBytes(prefix); got != 88 {
		t.Fatalf("77-byte payload allocation=%d want 88", got)
	}

	a.Free(single)
	a.Free(prefix)

	// Targeted buckets must remain safely reusable.
	single2 := a.Alloc(bytes.Repeat([]byte{'x'}, 16))
	prefix2 := a.Alloc(bytes.Repeat([]byte{'y'}, 77))
	if got := a.AllocationBytes(single2); got != 24 {
		t.Fatalf("reused singleton allocation=%d want 24", got)
	}
	if got := a.AllocationBytes(prefix2); got != 88 {
		t.Fatalf("reused prefix allocation=%d want 88", got)
	}
}

func TestTiny24ByteClassUsesTightFirstSegment(t *testing.T) {
	if got := segmentSizeForAllocation(24, 0); got != 192 {
		t.Fatalf("24-byte first segment=%d want 192", got)
	}
	if got := segmentSizeForAllocation(48, 0); got != 240 {
		t.Fatalf("48-byte first segment=%d want 240", got)
	}

	var a Arena
	value := bytes.Repeat([]byte{'x'}, 16)
	for i := 0; i < 8; i++ {
		a.Alloc(value)
	}
	if got := a.SegmentCount(); got != 1 {
		t.Fatalf("8 tiny values used %d segments, want 1", got)
	}
	if got := a.MemoryBytes(); got != 216 { // 192 data + 24 segment metadata
		t.Fatalf("8 tiny values use %d bytes, want 216", got)
	}

	projected := a.GrowthFor([]int{16})
	a.Alloc(value)
	if got := a.SegmentCount(); got != 2 {
		t.Fatalf("9 tiny values used %d segments, want 2", got)
	}
	if got := a.MemoryBytes(); got != 1248 { // 192 + 1008 data + 48 metadata
		t.Fatalf("9 tiny values use %d bytes, want 1248", got)
	}
	if projected != 1032 {
		t.Fatalf("ninth-value growth=%d want 1032", projected)
	}
}

func TestRefPacking(t *testing.T) {
	ref := newRef(
		(1<<refSegmentBits)-1,
		(1<<refOffsetBits)-1,
		32<<20,
		0x1122334455667788,
	)

	if ref.segment() != (1<<refSegmentBits)-1 {
		t.Fatalf("segment mismatch: %d", ref.segment())
	}

	if ref.offset() != (1<<refOffsetBits)-1 {
		t.Fatalf("offset mismatch: %d", ref.offset())
	}

	if ref.length() != 32<<20 {
		t.Fatalf("length mismatch: %d", ref.length())
	}

	if ref.generation != 0x1122334455667788 {
		t.Fatalf("generation mismatch: %x", ref.generation)
	}
}

func TestPackedRefSize(t *testing.T) {
	if got := unsafe.Sizeof(Ref{}); got != 16 {
		t.Fatalf("Ref size = %d, want 16", got)
	}
}

func TestLargeAllocations(t *testing.T) {
	sizes := []int{
		1 << 20,
		8 << 20,
		16 << 20,
		32 << 20,
	}

	for _, size := range sizes {
		t.Run(fmt.Sprintf("%d", size), func(t *testing.T) {
			var a Arena

			value := make([]byte, size)
			for i := range value {
				value[i] = byte(i)
			}

			ref := a.Alloc(value)

			got, err := a.View(ref)
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(got, value) {
				t.Fatal("large allocation round-trip mismatch")
			}

			if gotAllocation := a.AllocationBytes(ref); gotAllocation < uint64(size+8) {
				t.Fatalf("allocation too small: got %d need at least %d", gotAllocation, size+8)
			}

			a.Free(ref)
		})
	}
}
