package engine

import (
	"testing"
	"time"
)

func TestZSetLexRangeCountAndReverse(t *testing.T) {
	s := New()
	items := []ZSetItem{
		zitem(0, "alpha"),
		zitem(0, "beta"),
		zitem(0, "delta"),
		zitem(0, "gamma"),
	}
	if added, _, _, err := s.ZSetAdd("z", items, ZSetAddOptions{}); err != nil || added != 4 {
		t.Fatalf("add=%d err=%v", added, err)
	}
	min := ZSetLexBound{Value: []byte("beta")}
	max := ZSetLexBound{Value: []byte("gamma"), Exclusive: true}
	if count, err := s.ZSetLexCount("z", min, max); err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	got, err := s.ZSetRangeByLex("z", min, max, false, 0, -1)
	if err != nil || len(got) != 2 || string(got[0].Member) != "beta" || string(got[1].Member) != "delta" {
		t.Fatalf("range=%v err=%v", got, err)
	}
	rev, err := s.ZSetRangeByLex("z", ZSetLexBound{Infinite: -1}, ZSetLexBound{Infinite: 1}, true, 1, 2)
	if err != nil || len(rev) != 2 || string(rev[0].Member) != "delta" || string(rev[1].Member) != "beta" {
		t.Fatalf("reverse=%v err=%v", rev, err)
	}
}

func TestZSetRemoveRangeByLexPreservesTTL(t *testing.T) {
	s := New()
	if _, _, _, err := s.ZSetAdd("z", []ZSetItem{zitem(0, "a"), zitem(0, "b"), zitem(0, "c"), zitem(0, "d")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if !s.Expire("z", time.Minute) {
		t.Fatal("expire failed")
	}
	removed, err := s.ZSetRemoveRangeByLex("z", ZSetLexBound{Value: []byte("b")}, ZSetLexBound{Value: []byte("c")})
	if err != nil || removed != 2 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	if ttl := s.TTL("z", true); ttl <= 0 {
		t.Fatalf("ttl lost: %d", ttl)
	}
	items, err := s.ZSetRangeByLex("z", ZSetLexBound{Infinite: -1}, ZSetLexBound{Infinite: 1}, false, 0, -1)
	if err != nil || len(items) != 2 || string(items[0].Member) != "a" || string(items[1].Member) != "d" {
		t.Fatalf("items=%v err=%v", items, err)
	}
}

func TestZSetLexWrongTypeAndMissing(t *testing.T) {
	s := New()
	if count, err := s.ZSetLexCount("missing", ZSetLexBound{Infinite: -1}, ZSetLexBound{Infinite: 1}); err != nil || count != 0 {
		t.Fatalf("missing count=%d err=%v", count, err)
	}
	if err := s.SetWithTTL("plain", []byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ZSetLexCount("plain", ZSetLexBound{Infinite: -1}, ZSetLexBound{Infinite: 1}); err == nil {
		t.Fatal("expected wrong type")
	}
}
