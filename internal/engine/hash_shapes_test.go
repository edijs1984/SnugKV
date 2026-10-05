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

func firstShapedHashKey(t *testing.T, store *Store, keys []string) string {
	t.Helper()
	for _, key := range keys {
		if isShapedHash(physicalHashBytes(t, store, key)) {
			return key
		}
	}
	t.Fatalf("expected at least one shaped HASH among %d candidates", len(keys))
	return ""
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


func TestHashShapeMediumDirectLookup(t *testing.T) {
	store := New()
	fields, values := repeatedHashFixture(100, 64)

	keys := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		key := fmt.Sprintf("medium:%02d", i)
		keys = append(keys, key)
		if _, err := store.HashSet(key, fields, values); err != nil {
			t.Fatal(err)
		}
		physical := physicalHashBytes(t, store, key)
		if !isIndexedHash(physical) {
			t.Fatalf("expected active 100-field HASH %q to use indexed representation, got header %x", key, physical[:min(5, len(physical))])
		}
	}

	// Medium hashes stay indexed while active. Compaction observes repeated
	// layouts, admits the shared shape after the configured threshold, and
	// converts subsequently visited matching hashes. Map/shard iteration order
	// is intentionally unspecified, so select one key that actually converted.
	if compacted := store.Compact(^uint64(0)); compacted == 0 {
		t.Fatal("expected at least one shard to compact")
	}
	key := firstShapedHashKey(t, store, keys)

	for _, idx := range []int{0, 49, 99} {
		got, found, err := store.HashGet(key, fields[idx])
		if err != nil || !found || !bytes.Equal(got, values[idx]) {
			t.Fatalf("HGET field %d found=%v err=%v value mismatch=%v", idx, found, err, !bytes.Equal(got, values[idx]))
		}
	}
	if _, found, err := store.HashGet(key, []byte("field:missing")); err != nil || found {
		t.Fatalf("missing HGET found=%v err=%v", found, err)
	}

	query := [][]byte{fields[0], fields[50], fields[99], []byte("field:missing")}
	results := make([]HashGetResult, len(query))
	buf, err := store.HashGetResultsInto(key, query, nil, results)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if !results[i].Found {
			t.Fatalf("batched HGET field %d not found", i)
		}
		start := int(results[i].Offset)
		end := start + int(results[i].Length)
		if end > len(buf) || !bytes.Equal(buf[start:end], values[[]int{0, 50, 99}[i]]) {
			t.Fatalf("batched HGET field %d value mismatch", i)
		}
	}
	if results[3].Found {
		t.Fatal("missing batched HGET unexpectedly found")
	}

	gotValues, found, err := store.HashGetResults(key, query)
	if err != nil {
		t.Fatal(err)
	}
	for i, idx := range []int{0, 50, 99} {
		if !found[i] || !bytes.Equal(gotValues[i], values[idx]) {
			t.Fatalf("HashGetResults field %d mismatch", idx)
		}
	}
	if found[3] {
		t.Fatal("missing HashGetResults field unexpectedly found")
	}

	stats, ok, err := store.HashStorageStats(key)
	if err != nil || !ok {
		t.Fatalf("stats ok=%v err=%v", ok, err)
	}
	if stats.Encoding != "shape" {
		t.Fatalf("encoding=%q, want shape", stats.Encoding)
	}
	if stats.StoredBytes >= stats.PackedBytes {
		t.Fatalf("shape did not save memory: stored=%d packed=%d", stats.StoredBytes, stats.PackedBytes)
	}
}


func TestCompactConvertsColdMediumIndexedHashToShape(t *testing.T) {
	store := New()
	fields, values := repeatedHashFixture(100, 64)

	// Warm the shape catalog with the same completed layout while each active
	// hash still uses the indexed representation.
	keys := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		key := fmt.Sprintf("compact-medium:%02d", i)
		keys = append(keys, key)
		for j := range fields {
			if _, err := store.HashSet(key, fields[j:j+1], values[j:j+1]); err != nil {
				t.Fatal(err)
			}
		}
		physical := physicalHashBytes(t, store, key)
		if !isIndexedHash(physical) {
			t.Fatalf("active 100-field hash %q encoding=%x, want indexed", key, physical[:min(5, len(physical))])
		}
	}

	// Admission happens during compaction after repeated observations. Which key
	// crosses the threshold depends on map/shard iteration order, so validate an
	// actually converted key rather than assuming a fixed suffix wins admission.
	if compacted := store.Compact(^uint64(0)); compacted == 0 {
		t.Fatal("expected at least one shard to compact")
	}

	key := firstShapedHashKey(t, store, keys)
	physical := physicalHashBytes(t, store, key)

	for _, idx := range []int{0, 49, 99} {
		got, found, err := store.HashGet(key, fields[idx])
		if err != nil || !found || !bytes.Equal(got, values[idx]) {
			t.Fatalf("HGET field %d found=%v err=%v", idx, found, err)
		}
	}

	// First mutation after compaction must thaw back to the indexed write path.
	next := []byte("updated-value")
	if _, err := store.HashSet(key, fields[50:51], [][]byte{next}); err != nil {
		t.Fatal(err)
	}
	physical = physicalHashBytes(t, store, key)
	if !isIndexedHash(physical) {
		t.Fatalf("mutated medium hash encoding=%x, want indexed", physical[:min(5, len(physical))])
	}
	got, found, err := store.HashGet(key, fields[50])
	if err != nil || !found || !bytes.Equal(got, next) {
		t.Fatalf("updated HGET found=%v err=%v value=%q", found, err, got)
	}
}


func TestFixedWidthShapedHashDirectAddressing(t *testing.T) {
	store := New()
	fields, values := repeatedHashFixture(100, 64)
	keys := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		key := fmt.Sprintf("fixed-medium:%02d", i)
		keys = append(keys, key)
		for j := range fields {
			if _, err := store.HashSet(key, fields[j:j+1], values[j:j+1]); err != nil {
				t.Fatal(err)
			}
		}
	}
	store.Compact(^uint64(0))
	key := firstShapedHashKey(t, store, keys)
	physical := physicalHashBytes(t, store, key)
	if !isFixedShapedHash(physical) {
		t.Fatalf("expected fixed-width SH4 encoding, got header %x", physical[:min(5, len(physical))])
	}
	for _, idx := range []int{0, 1, 49, 98, 99} {
		got, found, err := store.HashGet(key, fields[idx])
		if err != nil || !found || !bytes.Equal(got, values[idx]) {
			t.Fatalf("HGET field %d found=%v err=%v", idx, found, err)
		}
	}
	records := store.Export([]string{key})
	if len(records) != 1 || !bytes.HasPrefix(records[0].Value, packedHashHeader[:]) {
		t.Fatal("fixed shaped HASH must export canonical packed representation")
	}
}

func TestVariableWidthShapedHashFallback(t *testing.T) {
	store := New()
	fields, values := repeatedHashFixture(8, 32)
	values[3] = append(values[3], 'x')
	for i := 0; i < 8; i++ {
		if _, err := store.HashSet(fmt.Sprintf("variable:%02d", i), fields, values); err != nil {
			t.Fatal(err)
		}
	}
	physical := physicalHashBytes(t, store, "variable:07")
	if !isShapedHash(physical) || isFixedShapedHash(physical) {
		t.Fatalf("expected variable-width SH2 encoding, got header %x", physical[:min(5, len(physical))])
	}
	for _, idx := range []int{0, 3, 7} {
		got, found, err := store.HashGet("variable:07", fields[idx])
		if err != nil || !found || !bytes.Equal(got, values[idx]) {
			t.Fatalf("HGET field %d found=%v err=%v", idx, found, err)
		}
	}
}


func TestHashPhysicalHeadersAreDistinct(t *testing.T) {
	headers := map[string][]byte{
		"packed":        packedHashHeader[:],
		"shape":         shapedHashHeader[:],
		"packed-expiry": packedHashExpiryHeader[:],
		"indexed":       indexedHashHeader[:],
		"shape-fixed":   shapedHashFixedHeader[:],
	}
	for nameA, a := range headers {
		for nameB, b := range headers {
			if nameA >= nameB {
				continue
			}
			if bytes.Equal(a, b) {
				t.Fatalf("HASH physical headers overlap: %s=%x %s=%x", nameA, a, nameB, b)
			}
		}
	}

	indexed, err := encodeIndexedHash([]HashPair{
		{Field: []byte("a"), Value: []byte("1")},
		{Field: []byte("b"), Value: []byte("2")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !isIndexedHash(indexed) || isShapedHash(indexed) {
		t.Fatalf("indexed HASH misclassified: %x", indexed[:min(5, len(indexed))])
	}

	fixedPairs := []HashPair{
		{Field: []byte("a"), Value: []byte("11")},
		{Field: []byte("b"), Value: []byte("22")},
	}
	fixed := encodeShapedHash(1, fixedPairs)
	if !isFixedShapedHash(fixed) || !isShapedHash(fixed) || isIndexedHash(fixed) || packedHashHasFieldExpiry(fixed) {
		t.Fatalf("fixed shaped HASH misclassified: %x", fixed[:min(5, len(fixed))])
	}

	expiring, err := encodePackedHash([]HashPair{
		{Field: []byte("a"), Value: []byte("1"), ExpiresAtMS: 123},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !packedHashHasFieldExpiry(expiring) || isIndexedHash(expiring) || isShapedHash(expiring) {
		t.Fatalf("expiring packed HASH misclassified: %x", expiring[:min(5, len(expiring))])
	}
}


func TestCompactConvertsColdLargeFixedHashToShape(t *testing.T) {
	store := New()
	fields, values := repeatedHashFixture(300, 64)
	keys := make([]string, 0, 8)

	for i := 0; i < 8; i++ {
		key := fmt.Sprintf("compact-large:%02d", i)
		keys = append(keys, key)
		for j := range fields {
			if _, err := store.HashSet(key, fields[j:j+1], values[j:j+1]); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Large hashes are HOT while mutating. Maintenance freezes them and may
	// compact repeated fixed-width layouts into SF1.
	if compacted := store.Compact(^uint64(0)); compacted == 0 {
		t.Fatal("expected at least one shard to compact")
	}

	key := firstShapedHashKey(t, store, keys)
	physical := physicalHashBytes(t, store, key)
	if !isFixedShapedHash(physical) {
		t.Fatalf("cold large HASH encoding=%x, want fixed shape", physical[:min(5, len(physical))])
	}

	for _, idx := range []int{0, 149, 299} {
		got, found, err := store.HashGet(key, fields[idx])
		if err != nil || !found || !bytes.Equal(got, values[idx]) {
			t.Fatalf("HGET field %d found=%v err=%v", idx, found, err)
		}
	}
}

func TestCompactKeepsColdLargeVariableHashIndexed(t *testing.T) {
	store := New()
	fields, values := repeatedHashFixture(300, 64)
	values[150] = append(values[150], 'x')

	keys := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		key := fmt.Sprintf("compact-large-variable:%02d", i)
		keys = append(keys, key)
		for j := range fields {
			if _, err := store.HashSet(key, fields[j:j+1], values[j:j+1]); err != nil {
				t.Fatal(err)
			}
		}
	}

	if compacted := store.Compact(^uint64(0)); compacted == 0 {
		t.Fatal("expected at least one shard to compact")
	}

	for _, key := range keys {
		physical := physicalHashBytes(t, store, key)
		if isShapedHash(physical) {
			t.Fatalf("variable-width large HASH %q should remain indexed, got %x", key, physical[:min(5, len(physical))])
		}
		if !isIndexedHash(physical) {
			t.Fatalf("variable-width large HASH %q encoding=%x, want indexed", key, physical[:min(5, len(physical))])
		}
	}
}
