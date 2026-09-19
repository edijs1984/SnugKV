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
