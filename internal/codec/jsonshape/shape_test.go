package jsonshape

import (
	"bytes"
	"testing"
)

func TestExactShapes(t *testing.T) {
	s := New(1<<20, 2)
	for _, value := range []string{`{"a":1,"a":-0,"b":"x\\\"y","c":[true,null,2e+03]}`, " \n{ \"x\" : \"\\u0061\" , \"nested\": {\"y\":false}}\n"} {
		s.Candidate([]byte(value))
		schema, slots, ok := s.Candidate([]byte(value))
		if !ok {
			t.Fatal("not admitted")
		}
		s.Retain(schema)
		got, err := Decode(schema, EncodeSlots(slots), len(value))
		if err != nil || string(got) != value {
			t.Fatalf("%q %v", got, err)
		}
		s.Release(schema)

		n, used := s.Stats()
		if n < 1 || used == 0 {
			t.Fatalf("released schema was not cached: count=%d used=%d", n, used)
		}

		// Cached zero-reference schema must remain immediately reusable
		// without going through the admission threshold again.
		cached, _, ok := s.Lookup([]byte(value))
		if !ok || cached != schema {
			t.Fatal("released schema was not reusable from cache")
		}
	}
}
func FuzzExactShape(f *testing.F) {
	for _, v := range []string{`{"a":1}`, `{"x":"\\u0000","x":null}`, `[true,false,1e100,-0]`, "null", "\x00"} {
		f.Add([]byte(v))
	}
	f.Fuzz(func(t *testing.T, v []byte) {
		literals, slots, ok := Split(v)
		if !ok {
			return
		}
		schema := &Schema{Literal: literals}
		out, err := Decode(schema, EncodeSlots(slots), len(v))
		if err != nil || !bytes.Equal(v, out) {
			t.Fatalf("roundtrip %v", err)
		}
	})
}


func TestDecodeIntoReusesDestination(t *testing.T) {
	store := New(1<<20, 1)
	value := []byte(`{"id":123456,"active":true,"name":"alice","missing":null}`)

	schema, slots, ok := store.Candidate(value)
	if !ok {
		t.Fatal("shape not admitted")
	}
	data := store.EncodeSlots(slots)
	dst := make([]byte, 0, len(value))

	got, err := DecodeInto(schema, data, len(value), dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, value) {
		t.Fatalf("round trip mismatch: got %q want %q", got, value)
	}
	if len(got) > 0 && &got[0] != &dst[:cap(dst)][0] {
		t.Fatal("DecodeInto did not reuse destination buffer")
	}
}
