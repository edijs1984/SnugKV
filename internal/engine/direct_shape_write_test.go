package engine

import (
	"fmt"
	"testing"
)

func TestKnownJSONShapeWritesDirectly(t *testing.T) {
	s, err := NewWithOptions(Options{
		Shards:        1,
		Encoding:      true,
		ShapeEncoding: true,
		Compression:   true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Current per-shard admission threshold is four.
	// These writes train and admit the shape.
	for i := 0; i < 4; i++ {
		value := []byte(fmt.Sprintf(
			`{"country":"LV","status":"active","plan":"free","user":%d,"long_repeated_property_name":true,"metadata":{"requestId":"abcdef0123456789","source":"nestjs"}}`,
			i,
		))

		if err := s.Set(fmt.Sprintf("warm:%d", i), value, 0); err != nil {
			t.Fatal(err)
		}
	}

	value := []byte(
		`{"country":"LV","status":"active","plan":"free","user":999,"long_repeated_property_name":true,"metadata":{"requestId":"abcdef0123456789","source":"nestjs"}}`,
	)

	// No optimizer / Rewrite call here.
	// This SET itself must use the cached schema.
	if err := s.Set("direct", value, 0); err != nil {
		t.Fatal(err)
	}

	name, _, encodedBytes, ok := s.Encoding("direct")
	if !ok {
		t.Fatal("direct key missing")
	}

	if name != "json-shape" {
		t.Fatalf(
			"known shape was not used directly: codec=%s encoded=%d",
			name,
			encodedBytes,
		)
	}

	if encodedBytes >= len(value) {
		t.Fatalf(
			"json-shape did not reduce value: encoded=%d logical=%d",
			encodedBytes,
			len(value),
		)
	}
}
