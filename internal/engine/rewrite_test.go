package engine

import (
	"bytes"
	"fmt"
	"snugkv/internal/codec"
	"testing"
	"time"
)

func TestRewriteRejectsStaleMutation(t *testing.T) {
	for _, mutation := range []string{"overwrite", "recreate", "ttl", "persist"} {
		t.Run(mutation, func(t *testing.T) {
			s := New()
			s.Set("k", []byte("123456789012345"), 60000)
			candidate, _ := s.Candidate("k", 1024)
			record := s.EncodeCandidate(candidate)
			s.encoding = true
			switch mutation {
			case "overwrite":
				s.Set("k", []byte("new"), 0)
			case "recreate":
				s.Delete("k")
				s.Set("k", candidate.Value, 0)
			case "ttl":
				s.Expire("k", time.Second)
			case "persist":
				s.Persist("k")
			}
			before, _ := s.Get("k")
			ttl := s.TTL("k", true)
			if s.Rewrite(candidate, record) {
				t.Fatal("stale rewrite accepted")
			}
			after, _ := s.Get("k")
			if string(before) != string(after) || s.TTL("k", true) > ttl {
				t.Fatal("stale rewrite changed state")
			}
			auditMemory(t, s)
		})
	}
}
func TestRewritePreservesTTL(t *testing.T) {
	s := New()
	now := time.Unix(100, 0)
	s.now = func() time.Time { return now }
	s.Set("k", []byte("123456789012345"), 60000)
	candidate, _ := s.Candidate("k", 1024)
	record := s.EncodeCandidate(candidate)
	s.encoding = true
	if !s.Rewrite(candidate, record) {
		t.Fatal("rewrite rejected")
	}
	if got, _ := s.Get("k"); string(got) != string(candidate.Value) || s.TTL("k", true) != 60000 {
		t.Fatal("rewrite changed logical state")
	}
	if s.Rewrite(candidate, record) {
		t.Fatal("same version accepted twice")
	}
	auditMemory(t, s)
}
func TestShapeSharingAndReclamation(t *testing.T) {
	s, _ := NewWithOptions(Options{Shards: 1, Encoding: true, ShapeEncoding: true})
	for i := 0; i < 10; i++ {
		key := fmt.Sprint(i)
		value := []byte(fmt.Sprintf(`{"country":"LV","status":"active","plan":"free","user":%d,"long_repeated_property_name":true}`, i))
		s.Set(key, value, 0)
		s.ObserveJSONShape(key, value)
		candidate, _ := s.Candidate(key, 4096)
		record := s.EncodeCandidate(candidate)
		s.Rewrite(candidate, record)
	}
	name, _, _, ok := s.Encoding("9")
	if !ok || name != "json-shape" {
		t.Fatalf("encoding %s", name)
	}
	for i := 0; i < 10; i++ {
		s.Delete(fmt.Sprint(i))
	}
	count, used := s.shards[0].shapes.Stats()
	if count != 1 || used == 0 {
		t.Fatalf("learned schema was not cached: count=%d used=%d", count, used)
	}

	// The cached schema is bounded metadata. It should remain available
	// after all live records using it have been deleted.
	value := []byte(`{"country":"LV","status":"active","plan":"free","user":999,"long_repeated_property_name":true}`)
	if _, _, ok := s.shards[0].shapes.Lookup(value); !ok {
		t.Fatal("cached schema disappeared after last live record")
	}

	auditMemory(t, s)
}
func TestRewriteRejectsChangedBytes(t *testing.T) {
	s, _ := NewWithOptions(Options{Shards: 1, Encoding: true})
	s.Set("k", []byte("arbitrary long value"), 0)
	candidate, _ := s.Candidate("k", 1024)
	if s.Rewrite(candidate, codec.Record{ID: codec.Raw, RawLength: 1, Data: []byte("x")}) {
		t.Fatal("changed logical bytes")
	}
}

func TestSamplingEventuallyVisitsEveryKey(t *testing.T) {
	s, _ := NewWithOptions(Options{Shards: 1})
	for i := 0; i < 100; i++ {
		s.Set(fmt.Sprint(i), []byte("v"), 0)
	}
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		for _, key := range s.SampleKeys(4) {
			seen[key] = true
		}
	}
	if len(seen) != 100 {
		t.Fatalf("visited %d keys", len(seen))
	}
}

func TestShapeEncodingSkipsPrimitiveValues(t *testing.T) {
	s, err := NewWithOptions(Options{
		Shards:        1,
		Encoding:      true,
		ShapeEncoding: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	values := [][]byte{
		[]byte("true"),
		[]byte("false"),
		[]byte("123456"),
		[]byte("1.25"),
		[]byte(`"hello"`),
		[]byte("plain text"),
	}

	for i, value := range values {
		key := fmt.Sprintf("k%d", i)

		if err := s.Set(key, value, 0); err != nil {
			t.Fatal(err)
		}

		candidate, ok := s.Candidate(key, 1<<20)
		if !ok {
			t.Fatalf("missing candidate for %q", value)
		}

		record := s.EncodeCandidate(candidate)
		if record.ID == 5 {
			t.Fatalf("primitive %q selected json-shape codec", value)
		}
	}

	if s.shards[0].shapes != nil {
		t.Fatal("primitive values eagerly created shape store")
	}

	memory := s.Memory()
	if memory.SchemaBytes != 0 {
		t.Fatalf("primitive values charged %d schema bytes", memory.SchemaBytes)
	}
}

func TestShapeStoreAllocatesLazily(t *testing.T) {
	s, err := NewWithOptions(Options{
		Shards:        1,
		Encoding:      true,
		ShapeEncoding: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	initial := s.Memory()
	if initial.SchemaBytes != 0 {
		t.Fatalf("new store charged %d schema bytes", initial.SchemaBytes)
	}

	for i := range s.shards {
		if s.shards[i].shapes != nil {
			t.Fatalf("shard %d eagerly created shape store", i)
		}
	}

	// Non-JSON values must not allocate shape state.
	if err := s.Set("plain", []byte("hello"), 0); err != nil {
		t.Fatal(err)
	}

	afterPlain := s.Memory()
	if afterPlain.SchemaBytes != 0 {
		t.Fatalf(
			"non-JSON SET unexpectedly charged %d schema bytes",
			afterPlain.SchemaBytes,
		)
	}

	key := "json-key"
	value := []byte(`{"country":"LV","status":"active","plan":"free","user":12345,"long_repeated_property_name":true}`)

	if err := s.Set(key, value, 0); err != nil {
		t.Fatal(err)
	}

	sh := s.shardFor(key)
	if sh.shapes != nil {
		t.Fatal("foreground JSON SET eagerly created shape store")
	}

	afterJSONSet := s.Memory()
	if afterJSONSet.SchemaBytes != 0 {
		t.Fatalf("foreground JSON SET charged %d schema bytes", afterJSONSet.SchemaBytes)
	}

	// Background optimizer observation creates the shared shape store.
	s.ObserveJSONShape(key, value)

	if sh.shapes == nil {
		t.Fatal("background JSON observation did not create shape store")
	}

	afterJSON := s.Memory()
	if afterJSON.SchemaBytes != shapeStoreBaseBytes {
		t.Fatalf(
			"schema bytes = %d, want %d",
			afterJSON.SchemaBytes,
			shapeStoreBaseBytes,
		)
	}

	// Additional observations on the same shard reuse the existing store.
	if err := s.Set("json-key-2", value, 0); err != nil {
		t.Fatal(err)
	}
	s.ObserveJSONShape("json-key-2", value)

	afterSecond := s.Memory()
	if afterSecond.SchemaBytes != shapeStoreBaseBytes {
		t.Fatalf(
			"second JSON observation charged schema base twice: got %d want %d",
			afterSecond.SchemaBytes,
			shapeStoreBaseBytes,
		)
	}
}

func TestLazyShapeStoreRespectsMaxMemory(t *testing.T) {
	const shards = 1
	base := structuralMemoryBytes(shards)

	s, err := NewWithOptions(Options{
		Shards:        shards,
		Encoding:      true,
		ShapeEncoding: true,
		MaxMemory:     base,
	})
	if err != nil {
		t.Fatal(err)
	}

	if s.shards[0].shapes != nil {
		t.Fatal("shape store allocated eagerly")
	}

	// Directly exercise lazy admission without adding key/value memory.
	candidate := Candidate{
		Key:   "k",
		Value: []byte(`{"country":"LV","status":"active","user":123}`),
	}

	record := s.EncodeCandidate(candidate)

	if s.shards[0].shapes != nil {
		t.Fatal("shape store allocated despite max-memory limit")
	}

	if record.ID == 5 {
		t.Fatal("json-shape selected without shape-store budget")
	}

	memory := s.Memory()
	if memory.AccountedBytes != base || memory.SchemaBytes != 0 {
		t.Fatalf(
			"unexpected accounting: used=%d schemas=%d",
			memory.AccountedBytes,
			memory.SchemaBytes,
		)
	}
}


func TestCandidateIntoReusesScratch(t *testing.T) {
	s, err := NewWithOptions(Options{Shards: 1, Encoding: true, Compression: true})
	if err != nil {
		t.Fatal(err)
	}

	value := []byte("abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if err := s.Set("k", value, 0); err != nil {
		t.Fatal(err)
	}

	scratch := make([]byte, 0, 256)
	candidate, ok := s.CandidateInto("k", 1024, scratch)
	if !ok {
		t.Fatal("missing candidate")
	}
	if string(candidate.Value) != string(value) {
		t.Fatalf("candidate=%q want=%q", candidate.Value, value)
	}
	if cap(candidate.Value) != cap(scratch) {
		t.Fatalf("candidate capacity=%d want scratch capacity=%d", cap(candidate.Value), cap(scratch))
	}
	if len(candidate.Value) > 0 && &candidate.Value[0] != &scratch[:cap(scratch)][0] {
		t.Fatal("CandidateInto did not reuse supplied scratch")
	}
}


func TestOptimizationClassForValue(t *testing.T) {
	s, err := NewWithOptions(Options{Shards: 1, Encoding: true, Compression: true, ShapeEncoding: true})
	if err != nil { t.Fatal(err) }
	tests := []struct { name string; value []byte; want OptimizationClass }{
		{"counter", []byte("1000000042"), OptimizationNone},
		{"uuid", []byte("123e4567-e89b-12d3-a456-426614174000"), OptimizationNone},
		{"json", []byte("{\"user\":1,\"active\":true,\"roles\":[\"admin\"]}"), OptimizationJSON},
		{"text", bytes.Repeat([]byte("hello-world-"), 32), OptimizationCompress},
		{"gzip", append([]byte{0x1f, 0x8b}, bytes.Repeat([]byte{0x42}, 300)...), OptimizationNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.OptimizationClassForValue(tt.value); got != tt.want { t.Fatalf("class=%v want=%v", got, tt.want) }
			if got := s.ShouldQueueOptimization(tt.value); got != (tt.want != OptimizationNone) { t.Fatalf("queue=%v class=%v", got, tt.want) }
		})
	}
}
