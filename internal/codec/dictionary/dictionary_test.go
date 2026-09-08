package dictionary

import (
	"bytes"
	"testing"
)

func TestAdmissionAndReclamation(t *testing.T) {
	s := New(4096)
	value := []byte("repeated-string-value")
	var id uint64
	var ok bool
	for i := 0; i < 32; i++ {
		id, ok = s.Candidate(value)
	}
	if !ok || !s.Retain([]uint64{id, id}) {
		t.Fatal("admission")
	}
	got, ok := s.Lookup(id)
	if !ok || !bytes.Equal(got, value) {
		t.Fatal("lookup")
	}
	got[0] = 'X'
	again, _ := s.Lookup(id)
	if !bytes.Equal(again, value) {
		t.Fatal("aliased output")
	}
	s.Release([]uint64{id, id})
	if n, used := s.Stats(); n != 0 || used != 0 {
		t.Fatal("leak")
	}
	if s.Retain([]uint64{id}) {
		t.Fatal("retained stale ID")
	}
}
