package codec

import (
	"bytes"
	"testing"
)

func TestBoolCodec(t *testing.T) {
	c := boolCodec{}

	tests := []struct {
		value   string
		encoded byte
	}{
		{"false", 0},
		{"true", 1},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			data, ok := c.Encode([]byte(tt.value))
			if !ok {
				t.Fatalf("Encode(%q) rejected valid bool", tt.value)
			}

			if len(data) != 1 {
				t.Fatalf("encoded length = %d, want 1", len(data))
			}

			if data[0] != tt.encoded {
				t.Fatalf(
					"encoded byte = %d, want %d",
					data[0],
					tt.encoded,
				)
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

	if _, err := c.Decode([]byte{}, 4); err == nil {
		t.Fatal("expected error for empty bool encoding")
	}

	if _, err := c.Decode([]byte{2}, 4); err == nil {
		t.Fatal("expected error for invalid bool byte")
	}

	if _, err := c.Decode([]byte{0, 1}, 4); err == nil {
		t.Fatal("expected error for oversized bool encoding")
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

			if len(rec.Data) != 1 {
				t.Fatalf(
					"stored length = %d, want 1",
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
