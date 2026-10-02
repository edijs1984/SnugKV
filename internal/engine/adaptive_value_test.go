package engine

import (
	"bytes"
	"testing"
)

func TestAdaptiveRawStringUsesDirectVisitorWithEncodingEnabled(t *testing.T) {
	store, err := NewWithOptions(Options{
		Shards:        1,
		Encoding:      true,
		Compression:   true,
		ShapeEncoding: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	value := bytes.Repeat([]byte("incompressible-ish-payload:"), 8)
	if len(value) <= 36 {
		t.Fatal("test value must bypass synchronous scalar codecs")
	}
	if err := store.Set("raw", value, 0); err != nil {
		t.Fatal(err)
	}

	var got []byte
	handled, err := store.VisitRawStringBytes([]byte("raw"), func(view []byte) error {
		got = append(got[:0], view...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("adaptive store did not use RAW direct visitor")
	}
	if !bytes.Equal(got, value) {
		t.Fatalf("value mismatch: got %q want %q", got, value)
	}
}

func TestAdaptiveEncodedScalarFallsBackFromRawVisitor(t *testing.T) {
	store, err := NewWithOptions(Options{
		Shards:   1,
		Encoding: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Set("n", []byte("123456"), 0); err != nil {
		t.Fatal(err)
	}

	handled, err := store.VisitRawStringBytes([]byte("n"), func([]byte) error {
		t.Fatal("encoded scalar must not use RAW direct visitor")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("encoded scalar unexpectedly reported RAW handling")
	}

	got, found, wrongType := store.GetStringBytesInto([]byte("n"), nil)
	if wrongType || !found || string(got) != "123456" {
		t.Fatalf("fallback get got=%q found=%t wrongType=%t", got, found, wrongType)
	}
}
