package codec

import (
	"bytes"
	"testing"
)

func TestFloat64CodecCanonicalValues(t *testing.T) {
	c := float64Codec{}

	values := []string{
		"0.1",
		"-0.1",
		"3.141592653589793",
		"1e+20",
		"1e-20",
		"-0",
	}

	for _, value := range values {
		t.Run(value, func(t *testing.T) {
			encoded, ok := c.Encode([]byte(value))
			if !ok {
				t.Fatalf("Encode(%q) rejected canonical float", value)
			}

			if len(encoded) != 8 {
				t.Fatalf("encoded length = %d, want 8", len(encoded))
			}

			decoded, err := c.Decode(encoded, len(value))
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

func TestFloat64CodecRejectsNonCanonicalValues(t *testing.T) {
	c := float64Codec{}

	values := []string{
		"",
		"1",
		"123",
		"1.0",
		"1.00",
		"01.5",
		"+1.5",
		"1e3",
		"1E+20",
		"NaN",
		"nan",
		"Inf",
		"+Inf",
		"-Inf",
	}

	for _, value := range values {
		t.Run(value, func(t *testing.T) {
			if _, ok := c.Encode([]byte(value)); ok {
				t.Fatalf("Encode(%q) unexpectedly accepted value", value)
			}
		})
	}
}

func TestRegistryChoosesFloat64WhenSmaller(t *testing.T) {
	r := NewRegistry()

	value := []byte("3.141592653589793")

	rec := r.Encode(value)

	if rec.ID != Float64 {
		t.Fatalf("codec ID = %d, want FLOAT64 (%d)", rec.ID, Float64)
	}

	if len(rec.Data) != 8 {
		t.Fatalf("stored length = %d, want 8", len(rec.Data))
	}

	decoded, err := r.Decode(rec, len(value))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(decoded, value) {
		t.Fatalf("round trip changed bytes: got %q want %q", decoded, value)
	}
}

func TestRegistryKeepsShortFloatRaw(t *testing.T) {
	r := NewRegistry()

	// Exact FLOAT64 encoding would require 8 bytes, so compact encoding
	// should not replace a shorter raw representation.
	value := []byte("0.1")

	rec := r.Encode(value)

	if rec.ID != Raw {
		t.Fatalf("codec ID = %d, want RAW", rec.ID)
	}

	decoded, err := r.Decode(rec, len(value))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(decoded, value) {
		t.Fatalf("round trip changed bytes: got %q want %q", decoded, value)
	}
}
