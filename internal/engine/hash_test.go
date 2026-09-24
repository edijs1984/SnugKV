package engine

import (
	"bytes"
	"testing"
	"time"
)

func TestPackedHashRoundTripBinarySafe(t *testing.T) {
	input := []HashPair{
		{Field: []byte("z"), Value: []byte{0x00, 0xff, 0x01}},
		{Field: []byte("a"), Value: []byte("first")},
		{Field: []byte{0x00, 0x01}, Value: []byte{}},
	}

	packed, err := encodePackedHash(input)
	if err != nil {
		t.Fatal(err)
	}

	got, err := decodePackedHash(packed)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(input) {
		t.Fatalf("pairs = %d, want %d", len(got), len(input))
	}

	// Packed fields are canonicalized into bytewise order.
	for i := 1; i < len(got); i++ {
		if bytes.Compare(got[i-1].Field, got[i].Field) >= 0 {
			t.Fatalf("fields are not sorted: %q >= %q", got[i-1].Field, got[i].Field)
		}
	}

	value, found, err := packedHashLookup(packed, []byte("z"), time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if !found || !bytes.Equal(value, []byte{0x00, 0xff, 0x01}) {
		t.Fatalf("lookup = %v %v", value, found)
	}
}

func TestHashSetGetLenOverwriteAndDuplicateInput(t *testing.T) {
	store := New()

	added, err := store.HashSet(
		"user:1",
		[][]byte{[]byte("name"), []byte("age"), []byte("name")},
		[][]byte{[]byte("Alice"), []byte("41"), []byte("Alicia")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 {
		t.Fatalf("added = %d, want 2", added)
	}

	length, err := store.HashLen("user:1")
	if err != nil {
		t.Fatal(err)
	}
	if length != 2 {
		t.Fatalf("length = %d, want 2", length)
	}

	name, found, err := store.HashGet("user:1", []byte("name"))
	if err != nil {
		t.Fatal(err)
	}
	if !found || string(name) != "Alicia" {
		t.Fatalf("name = %q found=%v, want Alicia", name, found)
	}

	added, err = store.HashSet(
		"user:1",
		[][]byte{[]byte("age")},
		[][]byte{[]byte("42")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 {
		t.Fatalf("overwrite added = %d, want 0", added)
	}

	age, found, err := store.HashGet("user:1", []byte("age"))
	if err != nil {
		t.Fatal(err)
	}
	if !found || string(age) != "42" {
		t.Fatalf("age = %q found=%v, want 42", age, found)
	}

	valueType, found := store.ValueTypeOf("user:1")
	if !found || valueType != TypeHash {
		t.Fatalf("type = %s found=%v, want HASH", valueType.String(), found)
	}
}

func TestHashSetPreservesTTL(t *testing.T) {
	store := New()
	if _, err := store.HashSet(
		"session",
		[][]byte{[]byte("user")},
		[][]byte{[]byte("123")},
	); err != nil {
		t.Fatal(err)
	}

	if !store.Expire("session", 5*time.Second) {
		t.Fatal("failed to set TTL")
	}
	before := store.TTL("session", true)

	if _, err := store.HashSet(
		"session",
		[][]byte{[]byte("role")},
		[][]byte{[]byte("member")},
	); err != nil {
		t.Fatal(err)
	}

	after := store.TTL("session", true)
	if before <= 0 || after <= 0 {
		t.Fatalf("TTL lost: before=%d after=%d", before, after)
	}
}

func TestHashWrongType(t *testing.T) {
	store := New()
	if err := store.Set("plain", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}

	if _, err := store.HashSet(
		"plain",
		[][]byte{[]byte("field")},
		[][]byte{[]byte("value")},
	); err == nil {
		t.Fatal("expected WRONGTYPE from HashSet")
	}

	if _, _, err := store.HashGet("plain", []byte("field")); err == nil {
		t.Fatal("expected WRONGTYPE from HashGet")
	}
}

func TestHashDeleteRemovesEmptyHash(t *testing.T) {
	store := New()
	if _, err := store.HashSet(
		"hash",
		[][]byte{[]byte("a"), []byte("b")},
		[][]byte{[]byte("1"), []byte("2")},
	); err != nil {
		t.Fatal(err)
	}

	deleted, err := store.HashDel(
		"hash",
		[][]byte{[]byte("missing"), []byte("a")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}

	deleted, err = store.HashDel("hash", [][]byte{[]byte("b")})
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}

	if _, found := store.ValueTypeOf("hash"); found {
		t.Fatal("empty hash key should be removed")
	}
}

func TestHashFieldExpiryPackedRoundTripAndVisibility(t *testing.T) {
	store := New()
	if _, err := store.HashSet(
		"hfe",
		[][]byte{[]byte("alive"), []byte("expired"), []byte("persistent")},
		[][]byte{[]byte("1"), []byte("2"), []byte("3")},
	); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UnixMilli()
	results, err := store.HashFieldExpireAt(
		"hfe",
		[][]byte{[]byte("alive"), []byte("expired")},
		now+60000,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0] != 1 || results[1] != 1 {
		t.Fatalf("expire results = %v", results)
	}
	results, err = store.HashFieldExpireAt("hfe", [][]byte{[]byte("expired")}, now-1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0] != 2 {
		t.Fatalf("immediate expiry result = %v", results)
	}

	if _, found, err := store.HashGet("hfe", []byte("expired")); err != nil || found {
		t.Fatalf("expired field found=%v err=%v", found, err)
	}
	if value, found, err := store.HashGet("hfe", []byte("alive")); err != nil || !found || string(value) != "1" {
		t.Fatalf("alive field value=%q found=%v err=%v", value, found, err)
	}

	ttl, err := store.HashFieldPTTL("hfe", [][]byte{[]byte("alive"), []byte("persistent"), []byte("expired")})
	if err != nil {
		t.Fatal(err)
	}
	if len(ttl) != 3 || ttl[0] <= 0 || ttl[1] != -1 || ttl[2] != -2 {
		t.Fatalf("field TTLs = %v", ttl)
	}

	records := store.Export(nil)
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if !packedHashHasFieldExpiry(records[0].Value) {
		t.Fatalf("expected HFE packed hash header: %x", records[0].Value[:3])
	}

	restored := New()
	if err := restored.Restore(records, false); err != nil {
		t.Fatal(err)
	}
	ttl, err = restored.HashFieldPTTL("hfe", [][]byte{[]byte("alive"), []byte("persistent")})
	if err != nil {
		t.Fatal(err)
	}
	if ttl[0] <= 0 || ttl[1] != -1 {
		t.Fatalf("restored field TTLs = %v", ttl)
	}

	if _, err := restored.HashSet("hfe", [][]byte{[]byte("alive")}, [][]byte{[]byte("updated")}); err != nil {
		t.Fatal(err)
	}
	ttl, err = restored.HashFieldPTTL("hfe", [][]byte{[]byte("alive")})
	if err != nil {
		t.Fatal(err)
	}
	if ttl[0] != -1 {
		t.Fatalf("HSET should clear field TTL, got %v", ttl)
	}
}

func TestHashPersistenceRoundTrip(t *testing.T) {
	store := New()
	if _, err := store.HashSet(
		"profile",
		[][]byte{[]byte("name"), []byte("country")},
		[][]byte{[]byte("Edijs"), []byte("LV")},
	); err != nil {
		t.Fatal(err)
	}

	records := store.Export(nil)
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].ValueType != uint8(TypeHash) {
		t.Fatalf("persisted type = %d, want %d", records[0].ValueType, TypeHash)
	}

	restored := New()
	if err := restored.Restore(records, false); err != nil {
		t.Fatal(err)
	}

	country, found, err := restored.HashGet("profile", []byte("country"))
	if err != nil {
		t.Fatal(err)
	}
	if !found || string(country) != "LV" {
		t.Fatalf("country = %q found=%v", country, found)
	}

	valueType, found := restored.ValueTypeOf("profile")
	if !found || valueType != TypeHash {
		t.Fatalf("restored type = %s found=%v", valueType.String(), found)
	}
}

func TestHashStorageStats(t *testing.T) {
	store := New()
	if _, err := store.HashSet(
		"stats",
		[][]byte{[]byte("alpha"), []byte("beta")},
		[][]byte{[]byte("one"), []byte("two")},
	); err != nil {
		t.Fatal(err)
	}

	stats, found, err := store.HashStorageStats("stats")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("missing stats")
	}
	if stats.Fields != 2 || stats.FieldBytes != 9 || stats.ValueBytes != 6 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if stats.PackedBytes <= stats.FieldBytes+stats.ValueBytes {
		t.Fatalf("packed bytes must include framing: %+v", stats)
	}
}
