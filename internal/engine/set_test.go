package engine

import (
	"bytes"
	"testing"
	"time"
)

func TestPackedSetCoreSemantics(t *testing.T) {
	s := New()
	added, err := s.SetAdd("set", [][]byte{[]byte("b"), []byte("a"), []byte("b"), {0x00, 0xff}})
	if err != nil {
		t.Fatal(err)
	}
	if added != 3 {
		t.Fatalf("added=%d want 3", added)
	}

	members, err := s.SetMembers("set")
	if err != nil {
		t.Fatal(err)
	}
	want := [][]byte{{0x00, 0xff}, []byte("a"), []byte("b")}
	if len(members) != len(want) {
		t.Fatalf("members=%d want %d", len(members), len(want))
	}
	for i := range want {
		if !bytes.Equal(members[i], want[i]) {
			t.Fatalf("member[%d]=%q want %q", i, members[i], want[i])
		}
	}

	found, err := s.SetContains("set", []byte("a"))
	if err != nil || !found {
		t.Fatalf("contains a=%v err=%v", found, err)
	}
	found, err = s.SetContains("set", []byte("missing"))
	if err != nil || found {
		t.Fatalf("contains missing=%v err=%v", found, err)
	}

	length, err := s.SetLen("set")
	if err != nil || length != 3 {
		t.Fatalf("len=%d err=%v", length, err)
	}

	removed, err := s.SetRemove("set", [][]byte{[]byte("a"), []byte("missing")})
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
}

func TestSetMutationPreservesTTLAndDeletesEmptyKey(t *testing.T) {
	s := New()
	if _, err := s.SetAdd("set", [][]byte{[]byte("a")}); err != nil {
		t.Fatal(err)
	}
	if !s.Expire("set", time.Minute) {
		t.Fatal("Expire returned false")
	}
	before := s.TTL("set", true)
	if before <= 0 {
		t.Fatalf("ttl before=%d", before)
	}
	if _, err := s.SetAdd("set", [][]byte{[]byte("b")}); err != nil {
		t.Fatal(err)
	}
	after := s.TTL("set", true)
	if after <= 0 || after > before {
		t.Fatalf("ttl after=%d before=%d", after, before)
	}
	if removed, err := s.SetRemove("set", [][]byte{[]byte("a"), []byte("b")}); err != nil || removed != 2 {
		t.Fatalf("remove all=%d err=%v", removed, err)
	}
	if got := s.Type("set"); got != "none" {
		t.Fatalf("type after removing last member=%q", got)
	}
}

func TestSetWrongTypeAndOptimizerExclusion(t *testing.T) {
	s, err := NewWithOptions(Options{Shards: 16, Encoding: true, Compression: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("string", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAdd("string", [][]byte{[]byte("x")}); err == nil {
		t.Fatal("SADD on string did not return WRONGTYPE")
	}
	if _, err := s.SetAdd("native", [][]byte{[]byte("x"), []byte("y")}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Candidate("native", 1<<20); ok {
		t.Fatal("native SET became optimizer candidate")
	}
	if policy, ok := s.Policy("native"); !ok || policy != "set-native" {
		t.Fatalf("policy=%q ok=%v", policy, ok)
	}
	if stats := s.Memory(); stats.MetaBytes != 32 {
		// Only the ordinary string should own generic optimizer metadata.
		t.Fatalf("meta bytes=%d want 32", stats.MetaBytes)
	}
}
