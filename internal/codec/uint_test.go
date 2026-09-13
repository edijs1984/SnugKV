package codec

import (
	"bytes"
	"testing"
)

func TestUnsignedIntegerCodec(t *testing.T) {
	c := unsignedIntegerCodec{}

	values := [][]byte{
		[]byte("9223372036854775808"),
		[]byte("18446744073709551615"),
	}

	for _, value := range values {
		encoded, ok := c.Encode(value)
		if !ok {
			t.Fatalf("failed to encode %q", value)
		}

		if len(encoded) >= len(value) {
			t.Fatalf(
				"encoded %q is not smaller: %d >= %d",
				value,
				len(encoded),
				len(value),
			)
		}

		decoded, err := c.Decode(encoded, len(value))
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(decoded, value) {
			t.Fatalf(
				"round trip changed value: got %q want %q",
				decoded,
				value,
			)
		}
	}
}

func TestUnsignedIntegerCodecRejectsNonCanonical(t *testing.T) {
	c := unsignedIntegerCodec{}

	values := [][]byte{
		[]byte("00123"),
		[]byte("+123"),
		[]byte("00"),
		[]byte("9223372036854775807"),
		[]byte("18446744073709551616"),
		[]byte("-1"),
	}

	for _, value := range values {
		if _, ok := c.Encode(value); ok {
			t.Fatalf("unexpectedly encoded %q", value)
		}
	}
}

func TestRegistryChoosesUnsignedInteger(t *testing.T) {
	r := NewRegistry()

	value := []byte("18446744073709551615")

	rec := r.Encode(value)

	if rec.ID != UnsignedInteger {
		t.Fatalf(
			"codec = %v, want UnsignedInteger",
			rec.ID,
		)
	}

	decoded, err := r.Decode(rec, len(value))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(decoded, value) {
		t.Fatalf(
			"decoded = %q, want %q",
			decoded,
			value,
		)
	}
}
