package index

import (
	"fmt"
	"runtime"
	"testing"
	"unsafe"
)

func TestPackedSlotIs16Bytes(t *testing.T) {
	table := New[uint32]()
	if got := table.EntryBytes(); got != 16 {
		t.Fatalf("entry bytes = %d, want 16", got)
	}
}

func TestTableStructIs32Bytes(t *testing.T) {
	if got, want := unsafe.Sizeof(Table[uint32]{}), uintptr(32); got != want {
		t.Fatalf("table struct size = %d, want %d", got, want)
	}
}

func TestSparseInitialCapacityAndCompaction(t *testing.T) {
	table := New[uint32]()
	if got := table.GrowthBytes(1); got != 4*16 {
		t.Fatalf("first growth = %d, want %d", got, 4*16)
	}

	for i := 0; i < 3; i++ {
		table.Set(fmt.Sprintf("k%d", i), uint32(i))
	}
	if got := table.CapacityBytes(); got != 4*16 {
		t.Fatalf("three-key capacity = %d, want %d", got, 4*16)
	}
	if got := table.GrowthBytes(1); got != 0 {
		t.Fatalf("fourth-key growth = %d, want 0", got)
	}

	table.Set("k3", 3)
	if got := table.CapacityBytes(); got != 4*16 {
		t.Fatalf("four-key capacity = %d, want %d", got, 4*16)
	}
	if _, ok := table.Get("missing"); ok {
		t.Fatal("missing key found in full tiny table")
	}
	if got := table.GrowthBytes(1); got != 4*16 {
		t.Fatalf("fifth-key growth = %d, want %d", got, 4*16)
	}

	table.Set("k4", 4)
	if got := table.CapacityBytes(); got != 8*16 {
		t.Fatalf("five-key capacity = %d, want %d", got, 8*16)
	}

	table.Delete("k3")
	table.Delete("k4")
	table.Compact()
	if got := table.CapacityBytes(); got != 4*16 {
		t.Fatalf("compact three-key capacity = %d, want %d", got, 4*16)
	}
	for i := 0; i < 3; i++ {
		got, ok := table.Get(fmt.Sprintf("k%d", i))
		if !ok || got != uint32(i) {
			t.Fatalf("post-compact lookup %d got=%d ok=%t", i, got, ok)
		}
	}
}

func testGetHashed(table *Table[uint32], key string, hash uint64) (uint32, bool) {
	if len(table.slots) == 0 {
		return 0, false
	}
	mask := uint64(len(table.slots) - 1)
	for n := 0; n < len(table.slots); n++ {
		s := &table.slots[(hash+uint64(n))&mask]
		switch s.state() {
		case stateEmpty:
			return 0, false
		case stateLive:
			if s.keyLen() == len(key) && s.key() == key {
				return s.value(), true
			}
		}
	}
	return 0, false
}

func testInsertHashed(table *Table[uint32], key string, value uint32, hash uint64) {
	mask := uint64(len(table.slots) - 1)
	deleted := -1
	for n := 0; n < len(table.slots); n++ {
		i := int((hash + uint64(n)) & mask)
		s := &table.slots[i]
		switch s.state() {
		case stateLive:
			if s.keyLen() == len(key) && s.key() == key {
				s.setLive(key, value)
				return
			}
		case stateDeleted:
			if deleted < 0 {
				deleted = i
			}
		case stateEmpty:
			if deleted >= 0 {
				s = &table.slots[deleted]
			}
			s.setLive(key, value)
			table.count++
			return
		}
	}
	if deleted >= 0 {
		table.slots[deleted].setLive(key, value)
		table.count++
		return
	}
	panic("index capacity invariant")
}

func testSetHashed(table *Table[uint32], key string, value uint32, hash uint64) {
	if _, ok := testGetHashed(table, key, hash); !ok {
		capacity := table.capacityFor(table.count + 1)
		if capacity != len(table.slots) {
			old := table.slots
			table.slots = make([]slot[uint32], capacity)
			table.count = 0
			for i := range old {
				s := &old[i]
				if s.state() == stateLive {
					testInsertHashed(table, s.key(), s.value(), hash)
				}
			}
		}
	}
	testInsertHashed(table, key, value, hash)
}

func testDeleteHashed(table *Table[uint32], key string, hash uint64) {
	if len(table.slots) == 0 {
		return
	}
	mask := uint64(len(table.slots) - 1)
	for n := 0; n < len(table.slots); n++ {
		s := &table.slots[(hash+uint64(n))&mask]
		switch s.state() {
		case stateEmpty:
			return
		case stateLive:
			if s.keyLen() == len(key) && s.key() == key {
				s.setDeleted()
				table.count--
				return
			}
		}
	}
}

func testCompactHashed(table *Table[uint32], hash uint64) {
	capacity := initialCapacity
	if table.count == 0 {
		table.slots = nil
		return
	}
	for !capacityAccepts(table.count, capacity) {
		capacity *= 2
	}
	old := table.slots
	table.slots = make([]slot[uint32], capacity)
	table.count = 0
	for i := range old {
		s := &old[i]
		if s.state() == stateLive {
			testInsertHashed(table, s.key(), s.value(), hash)
		}
	}
}

func TestFullTinyTableRemainsCollisionSafe(t *testing.T) {
	table := New[uint32]()
	const hash = uint64(3)

	for i, key := range []string{"alpha", "beta", "gamma", "delta"} {
		testSetHashed(table, key, uint32(i+1), hash)
	}
	if got := table.CapacityBytes(); got != 4*16 {
		t.Fatalf("full tiny capacity = %d, want %d", got, 4*16)
	}
	if _, ok := testGetHashed(table, "missing", hash); ok {
		t.Fatal("missing colliding key returned from full table")
	}

	testSetHashed(table, "gamma", 99, hash)
	if got, ok := testGetHashed(table, "gamma", hash); !ok || got != 99 {
		t.Fatalf("full-table update got=%d ok=%t", got, ok)
	}
	if got := table.CapacityBytes(); got != 4*16 {
		t.Fatalf("update grew tiny table to %d bytes", got)
	}

	testSetHashed(table, "epsilon", 5, hash)
	if got := table.CapacityBytes(); got != 8*16 {
		t.Fatalf("fifth colliding key capacity = %d, want %d", got, 8*16)
	}
	for key, want := range map[string]uint32{
		"alpha": 1,
		"beta": 2,
		"gamma": 99,
		"delta": 4,
		"epsilon": 5,
	} {
		got, ok := testGetHashed(table, key, hash)
		if !ok || got != want {
			t.Fatalf("%s got=%d ok=%t want=%d", key, got, ok, want)
		}
	}
}

func TestCollisionChurn(t *testing.T) {
	table := New[uint32]()
	const hash = uint64(7)
	for i := 0; i < 1000; i++ {
		testSetHashed(table, fmt.Sprint(i), uint32(i), hash)
	}
	for i := 0; i < 1000; i += 2 {
		testDeleteHashed(table, fmt.Sprint(i), hash)
	}
	for i := 1; i < 1000; i += 2 {
		v, ok := testGetHashed(table, fmt.Sprint(i), hash)
		if !ok || v != uint32(i) {
			t.Fatal("collision lost key")
		}
	}
	before := table.CapacityBytes()
	testCompactHashed(table, hash)
	if table.CapacityBytes() > before {
		t.Fatal("compaction grew index")
	}
	for i := 0; i < 1000; i += 2 {
		testSetHashed(table, fmt.Sprint(i), uint32(i), hash)
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
	const hash = uint64(1)

	testSetHashed(table, "alpha", 1, hash)
	testSetHashed(table, "beta", 2, hash)
	testSetHashed(table, "gamma", 3, hash)
	testDeleteHashed(table, "beta", hash)
	testSetHashed(table, "delta", 4, hash)

	for key, want := range map[string]uint32{
		"alpha": 1,
		"gamma": 3,
		"delta": 4,
	} {
		got, ok := testGetHashed(table, key, hash)
		if !ok || got != want {
			t.Fatalf("%s got=%d ok=%t want=%d", key, got, ok, want)
		}
	}
	if _, ok := testGetHashed(table, "beta", hash); ok {
		t.Fatal("deleted colliding key returned")
	}
}
