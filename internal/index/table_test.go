package index

import (
	"fmt"
	"runtime"
	"testing"
)

func TestPackedSlotIs16Bytes(t *testing.T) {
	table := New[uint32]()
	if got := table.EntryBytes(); got != 16 {
		t.Fatalf("entry bytes = %d, want 16", got)
	}
}

func TestCollisionChurn(t *testing.T) {
	table := New[uint32]()
	table.hash = func(string) uint64 { return 7 }
	for i := 0; i < 1000; i++ {
		table.Set(fmt.Sprint(i), uint32(i))
	}
	for i := 0; i < 1000; i += 2 {
		table.Delete(fmt.Sprint(i))
	}
	for i := 1; i < 1000; i += 2 {
		v, ok := table.Get(fmt.Sprint(i))
		if !ok || v != uint32(i) {
			t.Fatal("collision lost key")
		}
	}
	before := table.CapacityBytes()
	table.Compact()
	if table.CapacityBytes() > before {
		t.Fatal("compaction grew index")
	}
	for i := 0; i < 1000; i += 2 {
		table.Set(fmt.Sprint(i), uint32(i))
	}
	if table.Len() != 1000 {
		t.Fatal(table.Len())
	}
	for key, value := range table.All() {
		if key != fmt.Sprint(value) {
			t.Fatal("iterator mismatch")
		}
	}
}

func TestGrowthAccounting(t *testing.T) {
	table := New[uint32]()
	for i := 0; i < 100; i++ {
		before := table.CapacityBytes()
		growth := table.GrowthBytes(1)
		table.Set(fmt.Sprint(i), uint32(i))
		if before+growth != table.CapacityBytes() {
			t.Fatal("growth mismatch")
		}
	}
}

func TestPackedSlotPreservesFullUint32ValueAndEmptyKey(t *testing.T) {
	table := New[uint32]()
	const max = ^uint32(0)
	table.Set("", max)
	got, ok := table.Get("")
	if !ok || got != max {
		t.Fatalf("empty-key lookup got=%d ok=%t", got, ok)
	}
	table.Set("", 17)
	got, ok = table.Get("")
	if !ok || got != 17 {
		t.Fatalf("empty-key overwrite got=%d ok=%t", got, ok)
	}
	table.Delete("")
	if _, ok := table.Get(""); ok {
		t.Fatal("empty key survived delete")
	}
}

func TestPackedSlotKeyBytesSurviveGCAndRehash(t *testing.T) {
	table := New[uint32]()
	const keys = 5000
	for i := 0; i < keys; i++ {
		// Build each key dynamically so the table's pointer is the only durable
		// reference to these particular backing bytes after the loop iteration.
		key := fmt.Sprintf("gc-key-%08d-payload", i)
		table.Set(key, uint32(i))
	}

	runtime.GC()
	runtime.Gosched()
	runtime.GC()

	for i := 0; i < keys; i++ {
		key := fmt.Sprintf("gc-key-%08d-payload", i)
		got, ok := table.Get(key)
		if !ok || got != uint32(i) {
			t.Fatalf("post-GC lookup %d got=%d ok=%t", i, got, ok)
		}
	}

	for i := 0; i < keys; i += 3 {
		table.Delete(fmt.Sprintf("gc-key-%08d-payload", i))
	}
	table.Compact()
	runtime.GC()

	for i := 0; i < keys; i++ {
		_, ok := table.Get(fmt.Sprintf("gc-key-%08d-payload", i))
		if (i%3 != 0) != ok {
			t.Fatalf("post-compact membership %d ok=%t", i, ok)
		}
	}
}

func TestPackedSlotTombstonesRemainCollisionSafe(t *testing.T) {
	table := New[uint32]()
	table.hash = func(string) uint64 { return 1 }

	table.Set("alpha", 1)
	table.Set("beta", 2)
	table.Set("gamma", 3)
	table.Delete("beta")
	table.Set("delta", 4)

	for key, want := range map[string]uint32{
		"alpha": 1,
		"gamma": 3,
		"delta": 4,
	} {
		got, ok := table.Get(key)
		if !ok || got != want {
			t.Fatalf("%s got=%d ok=%t want=%d", key, got, ok, want)
		}
	}
	if _, ok := table.Get("beta"); ok {
		t.Fatal("deleted colliding key returned")
	}
}
