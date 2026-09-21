package engine

import (
	"fmt"
	"sync"
	"testing"
)

func newShapeTestStore(t *testing.T) *Store {
	t.Helper()

	s, err := NewWithOptions(Options{
		Shards:        16,
		Encoding:      true,
		ShapeEncoding: true,
		Compression:   true,
	})
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func TestJSONShapesAreGlobalAndUniqueAcrossShards(t *testing.T) {
	s := newShapeTestStore(t)
	value := []byte(`{"user":{"id":123,"name":"Alice"},"active":true,"country":"LV"}`)

	seenShards := make(map[*shard]struct{})

	for i := 0; i < 200; i++ {
		key := "global-shape-test-" + string(rune(i+1000))

		seenShards[s.shardFor(key)] = struct{}{}

		if err := s.Set(key, value, 0); err != nil {
			t.Fatal(err)
		}
		s.ObserveJSONShape(key, value)
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

func TestGlobalJSONShapeCatalogConcurrentSetAndFlush(t *testing.T) {
	s := newShapeTestStore(t)

	values := [][]byte{
		[]byte(`{"user":{"id":123,"name":"Alice"},"active":true,"country":"LV"}`),
		[]byte(`{"order":{"id":456,"total":99.95},"paid":true,"currency":"EUR"}`),
		[]byte(`{"device":{"id":"abc","os":"linux"},"online":true,"region":"eu"}`),
		[]byte(`{"event":{"id":789,"kind":"login"},"success":true,"source":"web"}`),
	}

	const (
		writers       = 48
		writesPerTask = 300
		flushes       = 40
	)

	errCh := make(chan error, writers)
	var wg sync.WaitGroup

	for worker := 0; worker < writers; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()

			for i := 0; i < writesPerTask; i++ {
				key := fmt.Sprintf("shape-race-%d-%d", worker, i)
				value := values[(worker+i)%len(values)]

				if err := s.Set(key, value, 0); err != nil {
					errCh <- fmt.Errorf("set %q: %w", key, err)
					return
				}
				s.ObserveJSONShape(key, value)
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < flushes; i++ {
			s.FlushDB()
		}
	}()

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}
	if t.Failed() {
		return
	}

	shapes, _ := s.JSONShapes(10000)
	seen := make(map[string]struct{}, len(shapes))
	for _, shape := range shapes {
		if _, exists := seen[shape.Key]; exists {
			t.Fatalf("duplicate global JSON shape %q", shape.Key)
		}
		seen[shape.Key] = struct{}{}
	}

	// A flush must discard both keys and learned optimization metadata.
	s.FlushDB()
	if _, total := s.JSONShapes(100); total != 0 {
		t.Fatalf("shape count after final FLUSHDB = %d, want 0", total)
	}
	if got := s.Memory().SchemaBytes; got != 0 {
		t.Fatalf("schema bytes after final FLUSHDB = %d, want 0", got)
	}

	// The global catalog must remain usable after being dropped and recreated.
	value := values[0]
	for i := 0; i < 200; i++ {
		key := fmt.Sprintf("shape-rebuild-%d", i)
		if err := s.Set(key, value, 0); err != nil {
			t.Fatal(err)
		}
		s.ObserveJSONShape(key, value)
	}

	if _, total := s.JSONShapes(100); total != 1 {
		t.Fatalf("global shape count after rebuild = %d, want 1", total)
	}
}
