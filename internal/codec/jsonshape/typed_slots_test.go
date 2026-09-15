package jsonshape

import (
	"bytes"
	"testing"
)

func TestTypedSlotsExactRoundTrip(t *testing.T) {
	value := []byte(`{"positive":12345,"negative":-42,"zero":0,"negativeZero":-0,"yes":true,"no":false,"nothing":null,"text":"12345"}`)

	store := New(16<<10, 1)

	schema, slots, ok := store.Candidate(value)
	if !ok {
		t.Fatal("shape was not admitted")
	}

	typed := store.EncodeSlots(slots)
	raw := EncodeSlots(slots)

	if len(typed) >= len(raw) {
		t.Fatalf("typed slots did not improve size: typed=%d raw=%d", len(typed), len(raw))
	}

	decoded, err := Decode(schema, typed, len(value))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(decoded, value) {
		t.Fatalf("round trip changed bytes:\ngot:  %q\nwant: %q", decoded, value)
	}

	if !store.RetainRecord(schema, typed) {
		t.Fatal("failed to retain typed record")
	}

	store.ReleaseRecord(schema, typed)
}
