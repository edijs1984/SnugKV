package engine

import (
	"fmt"
	"testing"
)

// Compact must fit each shard index to its live key count; power-of-two growth
// alone leaves the counter-style workload at roughly 2 index slots per key.
func TestCompactTightensIndexReservation(t *testing.T) {
	store := New()
	const keys = 200000
	for i := 0; i < keys; i++ {
		if err := store.SetPlain(fmt.Sprintf("counter:%d", i), []byte(fmt.Sprint(1000000000+i))); err != nil {
			t.Fatal(err)
		}
	}
	before := store.Memory()
	if store.Compact(1<<30) == 0 {
		t.Fatal("Compact rebuilt no shards")
	}
	after := store.Memory()
	if after.IndexReservedBytes >= before.IndexReservedBytes {
		t.Fatalf("index not tightened: before=%d after=%d", before.IndexReservedBytes, after.IndexReservedBytes)
	}
	perKey := float64(after.IndexReservedBytes) / keys
	if perKey > 22 {
		t.Fatalf("index still %.1f B/key after Compact, want <= 22", perKey)
	}
	for i := 0; i < keys; i += 997 {
		got, ok := store.Get(fmt.Sprintf("counter:%d", i))
		if !ok || string(got) != fmt.Sprint(1000000000+i) {
			t.Fatalf("key %d wrong after Compact: %q ok=%t", i, got, ok)
		}
	}
	t.Logf("index B/key: before=%.1f after=%.1f; entry B/key after=%.1f", float64(before.IndexReservedBytes)/keys, perKey, float64(after.EntryBytes)/keys)
	// Writes after compaction still work and grow the tight tables.
	for i := keys; i < keys+50000; i++ {
		if err := store.SetPlain(fmt.Sprintf("counter:%d", i), []byte("1")); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := store.Get(fmt.Sprintf("counter:%d", keys+49999)); !ok {
		t.Fatal("post-compaction insert lost")
	}
}
