package engine

import (
	"fmt"
	"testing"
)

func TestCompactPacksDeletedEntryHoles(t *testing.T) {
	store, err := NewWithShards(1)
	if err != nil {
		t.Fatal(err)
	}

	const total = 4096
	const keep = 1024
	for i := 0; i < total; i++ {
		key := fmt.Sprintf("k:%04d", i)
		if err := store.Set(key, []byte("value"), 0); err != nil {
			t.Fatal(err)
		}
	}
	for i := keep; i < total; i++ {
		if !store.Delete(fmt.Sprintf("k:%04d", i)) {
			t.Fatalf("delete %d failed", i)
		}
	}

	before := store.Layout()
	if before.EntryCount != keep {
		t.Fatalf("entry count before=%d want=%d", before.EntryCount, keep)
	}
	if before.EntryCapacity <= before.EntryCount {
		t.Fatalf("expected dense entry slack before compaction: %+v", before)
	}

	if compacted := store.Compact(1 << 30); compacted != 1 {
		t.Fatalf("compacted=%d want=1", compacted)
	}

	after := store.Layout()
	if after.EntryCount != keep {
		t.Fatalf("entry count after=%d want=%d", after.EntryCount, keep)
	}
	if after.EntryCapacity != after.EntryCount {
		t.Fatalf("entry capacity after=%d count=%d", after.EntryCapacity, after.EntryCount)
	}
	if after.EntryStorageBytes >= before.EntryStorageBytes {
		t.Fatalf("entry storage did not shrink: before=%d after=%d", before.EntryStorageBytes, after.EntryStorageBytes)
	}

	for i := 0; i < keep; i++ {
		key := fmt.Sprintf("k:%04d", i)
		got, ok := store.Get(key)
		if !ok || string(got) != "value" {
			t.Fatalf("key %s corrupted after compaction: ok=%v value=%q", key, ok, got)
		}
	}
}
