package engine

import (
	"fmt"
	"testing"
)

func TestKnownJSONShapeDefersEncodingToBackground(t *testing.T) {
	s, err := NewWithOptions(Options{
		Shards:        1,
		Encoding:      true,
		ShapeEncoding: true,
		Compression:   true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate background optimizer observations until the shape is admitted.
	for i := 0; i < 4; i++ {
		value := []byte(fmt.Sprintf(
			`{"country":"LV","status":"active","plan":"free","user":%d,"long_repeated_property_name":true,"metadata":{"requestId":"abcdef0123456789","source":"nestjs"}}`,
			i,
		))

		key := fmt.Sprintf("warm:%d", i)
		if err := s.Set(key, value, 0); err != nil {
			t.Fatal(err)
		}
		s.ObserveJSONShape(key, value)
	}

	value := []byte(
		`{"country":"LV","status":"active","plan":"free","user":999,"long_repeated_property_name":true,"metadata":{"requestId":"abcdef0123456789","source":"nestjs"}}`,
	)

	// Even with an admitted schema, foreground SET must remain cheap and publish
	// the normal raw representation. JSON parsing/slot encoding belongs to the
	// background optimizer.
	if err := s.Set("direct", value, 0); err != nil {
		t.Fatal(err)
	}

	name, _, encodedBytes, ok := s.Encoding("direct")
	if !ok {
		t.Fatal("direct key missing")
	}
	if name != "raw" || encodedBytes != len(value) {
		t.Fatalf(
			"foreground SET performed shape encoding: codec=%s encoded=%d logical=%d",
			name,
			encodedBytes,
			len(value),
		)
	}
	if !s.ShouldQueueOptimization(value) {
		t.Fatal("known JSON shape was not eligible for background optimization")
	}

	// Simulate the optimizer evaluation/rewrite. The already-admitted schema
	// should be selected immediately without requiring foreground encoding.
	candidate, ok := s.Candidate("direct", len(value))
	if !ok {
		t.Fatal("missing optimizer candidate")
	}
	record := s.EncodeCandidate(candidate)
	if record.Schema == nil {
		t.Fatalf("background encoding did not select admitted JSON shape: codec=%s", s.codecs.Name(record.ID))
	}
	if len(record.Data) >= len(value) {
		t.Fatalf(
			"json-shape did not reduce value: encoded=%d logical=%d",
			len(record.Data),
			len(value),
		)
	)
}
