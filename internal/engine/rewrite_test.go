package engine

import (
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
	if count != 0 || used != 0 {
		t.Fatalf("leaked schemas %d %d", count, used)
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

	schemas, _ := s.shards[0].shapes.Stats()
	if schemas != 0 {
		t.Fatalf("primitive values created %d schemas", schemas)
	}
}
