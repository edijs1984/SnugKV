package engine

import (
	"bytes"
	"fmt"
	"testing"
)

func repeatedHashFixture(fields int, valueBytes int) ([][]byte, [][]byte) {
	names := make([][]byte, fields)
	values := make([][]byte, fields)
	for i := 0; i < fields; i++ {
		names[i] = []byte(fmt.Sprintf("field:%04d", i))
		value := make([]byte, valueBytes)
		for j := range value {
			value[j] = byte('a' + i%26)
		}
		values[i] = value
	}
	return names, values
}

func physicalHashBytes(t *testing.T, store *Store, key string) []byte {
	t.Helper()
	sh := store.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok := sh.get(key)
	if !ok {
		t.Fatalf("missing key %q", key)
	}
	return append([]byte(nil), sh.encoded(e)...)
}

func TestHashShapeAdmissionKeepsLogicalSemantics(t *testing.T) {
	store := New()
	fields, values := repeatedHashFixture(8, 32)

	for i := 0; i < 6; i++ {
		if _, err := store.HashSet(fmt.Sprintf("hash:%d", i), fields, values); err != nil {
			t.Fatal(err)
		}
	}

	physical := physicalHashBytes(t, store, "hash:5")
	if !isShapedHash(physical) {
		t.Fatalf("expected shaped HASH, got header %x", physical[:min(5, len(physical))])
	}

	stats, found, err := store.HashStorageStats("hash:5")
	if err != nil || !found {
		t.Fatalf("stats found=%v err=%v", found, err)
	}
	if stats.Encoding != "shape" {
		t.Fatalf("encoding = %q, want shape", stats.Encoding)
	}
	if stats.StoredBytes >= stats.PackedBytes {
		t.Fatalf("stored bytes = %d, packed bytes = %d", stats.StoredBytes, stats.PackedBytes)
	}

	value, found, err := store.HashGet("hash:5", fields[3])
	if err != nil || !found {
		t.Fatalf("HGET found=%v err=%v", found, err)
	}
	if !bytes.Equal(value, values[3]) {
		t.Fatalf("HGET value mismatch")
	}

	pairs, err := store.HashGetAll("hash:5")
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != len(fields) {
		t.Fatalf("HGETALL fields = %d, want %d", len(pairs), len(fields))
	}

	records := store.Export([]string{"hash:5"})
	if len(records) != 1 {
		t.Fatalf("export records = %d, want 1", len(records))
	}
	if !bytes.HasPrefix(records[0].Value, packedHashHeader[:]) {
		t.Fatalf("export must contain canonical packed HASH, got %x", records[0].Value[:min(5, len(records[0].Value))])
	}
	if isShapedHash(records[0].Value) {
		t.Fatal("physical HASH shape leaked into persistence")
	}
}

func TestShapedHashDirectLookupAndMutation(t *testing.T) {
	store := New()
	fields, values := repeatedHashFixture(10, 64)
	for i := 0; i < 8; i++ {
		if _, err := store.HashSet(fmt.Sprintf("direct:%02d", i), fields, values); err != nil {
			t.Fatal(err)
		}
	}

	key := "direct:07"
	physical := physicalHashBytes(t, store, key)
	if !isShapedHash(physical) {
		t.Fatalf("expected shaped HASH, got %x", physical[:min(5, len(physical))])
	}

	value, found, err := store.shapedHashLookupView(physical, fields[4])
	if err != nil || !found || !bytes.Equal(value, values[4]) {
		t.Fatalf("direct lookup found=%v err=%v", found, err)
	}
	if _, found, err := store.shapedHashLookupView(physical, []byte("field:9999")); err != nil || found {
		t.Fatalf("missing lookup found=%v err=%v", found, err)
	}

	updated := bytes.Repeat([]byte{'z'}, 64)
	added, err := store.HashSet(key, [][]byte{fields[4]}, [][]byte{updated})
	if err != nil || added != 0 {
		t.Fatalf("overwrite added=%d err=%v", added, err)
	}
	got, found, err := store.HashGet(key, fields[4])
	if err != nil || !found || !bytes.Equal(got, updated) {
		t.Fatalf("updated lookup found=%v err=%v", found, err)
	}

	newField := []byte("field:9999")
	newValue := bytes.Repeat([]byte{'n'}, 64)
	added, err = store.HashSet(key, [][]byte{newField}, [][]byte{newValue})
	if err != nil || added != 1 {
		t.Fatalf("insert added=%d err=%v", added, err)
	}
	got, found, err = store.HashGet(key, newField)
	if err != nil || !found || !bytes.Equal(got, newValue) {
		t.Fatalf("insert lookup found=%v err=%v", found, err)
	}
}

func TestShapedHashBatchLookupAvoidsCanonicalDecode(t *testing.T) {
	store := New()
	fields, values := repeatedHashFixture(10, 64)
	for i := 0; i < 8; i++ {
		if _, err := store.HashSet(fmt.Sprintf("batch:%02d", i), fields, values); err != nil {
			t.Fatal(err)
		}
	}
	key := "batch:07"
	physical := physicalHashBytes(t, store, key)
	if !isShapedHash(physical) {
		t.Fatal("expected shaped hash")
	}

	results := make([]HashGetResult, len(fields))
	dst := make([]byte, 0, len(fields)*64)
	var err error
	allocs := testing.AllocsPerRun(1000, func() {
		dst = dst[:0]
		dst, err = store.HashGetResultsInto(key, fields, dst, results)
	})
	if err != nil {
		t.Fatal(err)
	}
	if allocs > 0.1 {
		t.Fatalf("batch shaped HGET allocations/run=%v want ~0 with reused dst/results", allocs)
	}
	for i, result := range results {
		if !result.Found {
			t.Fatalf("field %d not found", i)
		}
		got := dst[result.Offset : result.Offset+result.Length]
		if !bytes.Equal(got, values[i]) {
			t.Fatalf("field %d value mismatch", i)
		}
	}
}

func TestHashShapeRestoreRebuildsPhysicalEncoding(t *testing.T) {
	store := New()
	fields, values := repeatedHashFixture(8, 32)
	for i := 0; i < 8; i++ {
		if _, err := store.HashSet(fmt.Sprintf("profile:%02d", i), fields, values); err != nil {
			t.Fatal(err)
		}
	}

	records := store.Export(nil)
	restored := New()
	if err := restored.Restore(records, false); err != nil {
		t.Fatal(err)
	}

	physical := physicalHashBytes(t, restored, "profile:07")
	if !isShapedHash(physical) {
		t.Fatalf("restored HASH did not rebuild shared shape: %x", physical[:min(5, len(physical))])
	}
	value, found, err := restored.HashGet("profile:07", fields[6])
	if err != nil || !found || !bytes.Equal(value, values[6]) {
		t.Fatalf("restored HGET found=%v err=%v value=%q", found, err, value)
	}
}

func TestHashShapeSkipsTinySingleFieldHashes(t *testing.T) {
	store := New()
	fields, values := repeatedHashFixture(1, 32)
	for i := 0; i < 8; i++ {
		if _, err := store.HashSet(fmt.Sprintf("tiny:%d", i), fields, values); err != nil {
			t.Fatal(err)
		}
	}

	physical := physicalHashBytes(t, store, "tiny:7")
	if isShapedHash(physical) {
		t.Fatal("single-field HASH should stay packed when savings are too small")
	}
	if !bytes.HasPrefix(physical, packedHashHeader[:]) {
		t.Fatalf("unexpected tiny HASH header %x", physical[:min(5, len(physical))])
	}
}

func TestFlushDBDropsHashShapeCatalogAccounting(t *testing.T) {
	store := New()
	fields, values := repeatedHashFixture(8, 32)
	for i := 0; i < 6; i++ {
		if _, err := store.HashSet(fmt.Sprintf("flush:%d", i), fields, values); err != nil {
			t.Fatal(err)
		}
	}
	before := store.Memory()
	if before.SchemaBytes == 0 {
		t.Fatal("expected HASH shape accounting")
	}

	store.FlushDB()
	after := store.Memory()
	if after.SchemaBytes != 0 {
		t.Fatalf("schema bytes after FLUSHDB = %d, want 0", after.SchemaBytes)
	}
}
