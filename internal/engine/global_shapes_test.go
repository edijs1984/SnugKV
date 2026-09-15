package engine

import "testing"

func TestJSONShapesAreGlobalAndUniqueAcrossShards(t *testing.T) {
	s, err := NewWithOptions(Options{
		Shards:        16,
		Encoding:      true,
		ShapeEncoding: true,
		Compression:   true,
	})
	if err != nil {
		t.Fatal(err)
	}

	value := []byte(`{"user":{"id":123,"name":"Alice"},"active":true,"country":"LV"}`)

	seenShards := make(map[*shard]struct{})

	for i := 0; i < 200; i++ {
		key := "global-shape-test-" + string(rune(i+1000))

		seenShards[s.shardFor(key)] = struct{}{}

		if err := s.Set(key, value, 0); err != nil {
			t.Fatal(err)
		}
	}

	if len(seenShards) < 2 {
		t.Fatal("test did not distribute keys across multiple shards")
	}

	_, total := s.JSONShapes(100)

	if total != 1 {
		t.Fatalf("global shape count = %d, want 1", total)
	}

	s.FlushDB()

	_, total = s.JSONShapes(100)

	if total != 0 {
		t.Fatalf("shape count after FLUSHDB = %d, want 0", total)
	}
}
