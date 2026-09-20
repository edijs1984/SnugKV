package engine

import (
	"testing"
	"time"
)

func TestStoreExactRoundTrip(t *testing.T) {
	store := New()
	value := []byte("{\"country\":\"LV\",\"status\":\"active\"}\n")

	if err := store.Set("session", value, 0); err != nil {
		t.Fatalf("Set returned error: %v", err)
	}

	got, ok := store.Get("session")
	if !ok {
		t.Fatal("expected key to exist")
	}

	if string(got) != string(value) {
		t.Fatalf("round trip mismatch: got %q want %q", got, value)
	}
}

func TestTTLExpiration(t *testing.T) {
	store := New()
	value := []byte("expires")

	if err := store.SetWithTTL("temp", value, 50*time.Millisecond); err != nil {
		t.Fatalf("SetWithTTL returned error: %v", err)
	}

	time.Sleep(120 * time.Millisecond)

	if _, ok := store.Get("temp"); ok {
		t.Fatal("expected expired key to be absent")
	}
}

func TestIncrBasic(t *testing.T) {
	store := New()

	if err := store.Set("counter", []byte("7"), 0); err != nil {
		t.Fatalf("Set returned error: %v", err)
	}

	got, err := store.Incr("counter")
	if err != nil {
		t.Fatalf("Incr returned error: %v", err)
	}
	if got != 8 {
		t.Fatalf("Incr result mismatch: got %d want %d", got, 8)
	}
}


func TestGetStringIntoPersistsActivityThroughSharedMetadata(t *testing.T) {
	store := New()
	store.encoding = true
	now := time.Unix(1_700_000_000, 0)
	store.now = func() time.Time { return now }

	if err := store.Set("hot", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}

	sh := store.shardFor("hot")
	sh.mu.Lock()
	e, ok := sh.get("hot")
	if !ok {
		sh.mu.Unlock()
		t.Fatal("missing setup key")
	}
	e.entryMeta = &entryMeta{}
	sh.set("hot", e)
	sh.mu.Unlock()

	value, found, wrongType := store.GetStringInto("hot", nil)
	if !found || wrongType || string(value) != "value" {
		t.Fatalf("GetStringInto value=%q found=%t wrongType=%t", value, found, wrongType)
	}

	sh.mu.RLock()
	got, ok := sh.get("hot")
	sh.mu.RUnlock()
	if !ok || got.entryMeta == nil {
		t.Fatal("metadata missing after GET")
	}
	if got.entryMeta.reads != 1 {
		t.Fatalf("reads=%d want 1", got.entryMeta.reads)
	}
	if got.entryMeta.lastAccess.IsZero() {
		t.Fatal("last access was not persisted")
	}
}

func TestGetStringBytesIntoBinaryKeyAndExpiry(t *testing.T) {
	store := New()
	store.encoding = true
	now := time.Unix(1_700_000_000, 0)
	store.now = func() time.Time { return now }

	key := "binary:\x00key"
	if err := store.Set(key, []byte("value"), 0); err != nil {
		t.Fatal(err)
	}
	value, found, wrongType := store.GetStringBytesInto([]byte(key), nil)
	if !found || wrongType || string(value) != "value" {
		t.Fatalf("persistent byte GET value=%q found=%t wrongType=%t", value, found, wrongType)
	}

	if err := store.SetWithTTL("expiring", []byte("ttl"), time.Second); err != nil {
		t.Fatal(err)
	}
	if value, found, wrongType := store.GetStringBytesInto([]byte("expiring"), nil); !found || wrongType || string(value) != "ttl" {
		t.Fatalf("live expiring byte GET value=%q found=%t wrongType=%t", value, found, wrongType)
	}

	now = now.Add(time.Second)
	if value, found, wrongType := store.GetStringBytesInto([]byte("expiring"), nil); found || wrongType || value != nil {
		t.Fatalf("expired byte GET value=%q found=%t wrongType=%t", value, found, wrongType)
	}
}

func TestGetStringRejectsStream(t *testing.T) {
	store := New()
	if _, _, err := store.StreamAdd(
		"events",
		"1-0",
		[]StreamField{{Field: []byte("field"), Value: []byte("value")}},
		StreamAddOptions{},
	); err != nil {
		t.Fatal(err)
	}

	if _, found, wrongType := store.GetString("events"); found || !wrongType {
		t.Fatalf("GetString stream found=%t wrongType=%t", found, wrongType)
	}
}
