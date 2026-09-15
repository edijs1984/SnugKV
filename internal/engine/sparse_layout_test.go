package engine

import (
	"fmt"
	"testing"
)

func TestSparseLayoutKeepsEntrySlackBounded(t *testing.T) {
	s := New()
	for i := 0; i < 1000; i++ {
		if err := s.Set(fmt.Sprintf("key:%04d", i), []byte("v"), 0); err != nil {
			t.Fatal(err)
		}
	}

	layout := s.SparseLayout()
	if layout.EntrySlotsUsed != 1000 {
		t.Fatalf("used entry slots = %d, want 1000", layout.EntrySlotsUsed)
	}
	if layout.ActiveShards == 0 || layout.ActiveShards > 256 {
		t.Fatalf("active shards = %d", layout.ActiveShards)
	}
	// The old 64-slot first reservation produced 16,384 slots with this
	// workload. Adaptive small pools should keep total capacity close to the
	// live key count even though keys are spread across many shards.
	if layout.EntryCapacity >= 4096 {
		t.Fatalf("entry capacity = %d, sparse reservation regression", layout.EntryCapacity)
	}
	if layout.EntryFreeSlots != layout.EntryCapacity-layout.EntrySlotsUsed {
		t.Fatalf("free slots = %d capacity=%d used=%d", layout.EntryFreeSlots, layout.EntryCapacity, layout.EntrySlotsUsed)
	}
}

func TestSparseLayoutTracksArenaActivation(t *testing.T) {
	s := New()
	for i := 0; i < 1000; i++ {
		if err := s.Set(fmt.Sprintf("arena:%04d", i), []byte("tiny"), 0); err != nil {
			t.Fatal(err)
		}
	}

	layout := s.SparseLayout()
	if layout.ArenaActiveShards == 0 || layout.ArenaActiveShards > layout.ShardCount {
		t.Fatalf("arena active shards = %d", layout.ArenaActiveShards)
	}
	if layout.ArenaSegments < layout.ArenaActiveShards {
		t.Fatalf("segments=%d active=%d", layout.ArenaSegments, layout.ArenaActiveShards)
	}

	memory := s.Memory()
	// The historical 8 KiB-first-segment behavior was roughly 2 MiB of arena
	// reservation for this 256-shard/1k-key shape. Keep a generous ceiling so
	// the test catches structural regressions without pinning allocator details.
	if memory.ArenaBytes >= 1<<20 {
		t.Fatalf("arena bytes = %d, expected sparse first-segment savings", memory.ArenaBytes)
	}
}
