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
		if n, _ := s.Stats(); n != 0 {
			t.Fatal("schema retained")
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
