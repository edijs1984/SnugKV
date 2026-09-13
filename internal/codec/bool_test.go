package codec

import (
	"bytes"
	"testing"
)

func TestBoolCodec(t *testing.T) {
	c := boolCodec{}

	tests := []struct {
		value string
	}{
		{"false"},
		{"true"},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			data, ok := c.Encode([]byte(tt.value))
			if !ok {
				t.Fatalf("Encode(%q) rejected valid bool", tt.value)
			}

			if len(data) != 0 {
				t.Fatalf("encoded length = %d, want 0", len(data))
			}

			decoded, err := c.Decode(data, len(tt.value))
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(decoded, []byte(tt.value)) {
				t.Fatalf(
					"round trip changed bytes: got %q want %q",
					decoded,
					tt.value,
				)
			}
		})
	}
}

func TestBoolCodecRejectsNonCanonicalValues(t *testing.T) {
	c := boolCodec{}

	values := []string{
		"",
		"TRUE",
		"FALSE",
		"True",
		"False",
		"1",
		"0",
		"yes",
		"no",
		" true",
		"false ",
	}

	for _, value := range values {
		t.Run(value, func(t *testing.T) {
			if _, ok := c.Encode([]byte(value)); ok {
				t.Fatalf("Encode(%q) unexpectedly accepted value", value)
			}
		})
	}
}

func TestBoolCodecRejectsCorruptEncoding(t *testing.T) {
	c := boolCodec{}

	// Empty payload is valid; the raw length distinguishes true/false.
	if got, err := c.Decode(nil, 4); err != nil || string(got) != "true" {
		t.Fatalf("decode true: got %q err %v", got, err)
	}

	if got, err := c.Decode(nil, 5); err != nil || string(got) != "false" {
		t.Fatalf("decode false: got %q err %v", got, err)
	}

	if _, err := c.Decode([]byte{1}, 4); err == nil {
		t.Fatal("expected error for non-empty bool payload")
	}

	if _, err := c.Decode(nil, 3); err == nil {
		t.Fatal("expected error for invalid bool length")
	}

	if _, err := c.Decode(nil, 6); err == nil {
		t.Fatal("expected error for invalid bool length")
	}
}

func TestRegistryChoosesBoolCodec(t *testing.T) {
	r := NewRegistry()

	for _, value := range []string{"true", "false"} {
		t.Run(value, func(t *testing.T) {
			rec := r.Encode([]byte(value))

			if rec.ID != Boolean {
				t.Fatalf(
					"codec ID = %d, want BOOL (%d)",
					rec.ID,
					Boolean,
				)
			}

			if len(rec.Data) != 0 {
				t.Fatalf(
					"stored length = %d, want 0",
					len(rec.Data),
				)
			}

			decoded, err := r.Decode(rec, len(value))
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(decoded, []byte(value)) {
				t.Fatalf(
					"round trip changed bytes: got %q want %q",
					decoded,
					value,
				)
			}
		})
	}
}
