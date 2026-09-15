package engine

import (
	"math"
	"testing"
	"time"
)

func TestZSetScoreRangeCountAndRead(t *testing.T) {
	s := New()
	items := []ZSetItem{
		zitem(1, "a"),
		zitem(2, "b"),
		zitem(2, "c"),
		zitem(3, "d"),
		zitem(4, "e"),
	}
	if _, _, _, err := s.ZSetAdd("z", items, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	min := ZSetScoreBound{Score: 2, Exclusive: true}
	max := ZSetScoreBound{Score: 4}
	count, err := s.ZSetCount("z", min, max)
	if err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	got, err := s.ZSetRangeByScore("z", ZSetScoreBound{Score: 2}, max, false, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got[0].Member) != "c" || string(got[1].Member) != "d" {
		t.Fatalf("range=%+v", got)
	}
	rev, err := s.ZSetRangeByScore("z", ZSetScoreBound{Score: 1}, ZSetScoreBound{Score: 3}, true, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"d", "c", "b", "a"}
	if len(rev) != len(want) {
		t.Fatalf("reverse=%+v", rev)
	}
	for i := range want {
		if string(rev[i].Member) != want[i] {
			t.Fatalf("reverse[%d]=%q want=%q", i, rev[i].Member, want[i])
		}
	}
	all, err := s.ZSetRangeByScore("z", ZSetScoreBound{Score: math.Inf(-1)}, ZSetScoreBound{Score: math.Inf(1)}, false, 0, -1)
	if err != nil || len(all) != 5 {
		t.Fatalf("all=%+v err=%v", all, err)
	}
}

func TestZSetRemoveRangesPreserveTTL(t *testing.T) {
	s := New()
	if _, _, _, err := s.ZSetAdd("z", []ZSetItem{
		zitem(1, "a"), zitem(2, "b"), zitem(3, "c"), zitem(4, "d"), zitem(5, "e"),
	}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if !s.Expire("z", time.Minute) {
		t.Fatal("expire failed")
	}
	removed, err := s.ZSetRemoveRangeByScore("z", ZSetScoreBound{Score: 2}, ZSetScoreBound{Score: 4, Exclusive: true})
	if err != nil || removed != 2 {
		t.Fatalf("score remove=%d err=%v", removed, err)
	}
	if ttl := s.TTL("z", true); ttl <= 0 {
		t.Fatalf("ttl lost after score removal: %d", ttl)
	}
	items, _ := s.ZSetRange("z", 0, -1, false)
	if len(items) != 3 || string(items[0].Member) != "a" || string(items[1].Member) != "d" || string(items[2].Member) != "e" {
		t.Fatalf("after score removal=%+v", items)
	}
	removed, err = s.ZSetRemoveRangeByRank("z", -2, -1)
	if err != nil || removed != 2 {
		t.Fatalf("rank remove=%d err=%v", removed, err)
	}
	if ttl := s.TTL("z", true); ttl <= 0 {
		t.Fatalf("ttl lost after rank removal: %d", ttl)
	}
	items, _ = s.ZSetRange("z", 0, -1, false)
	if len(items) != 1 || string(items[0].Member) != "a" {
		t.Fatalf("after rank removal=%+v", items)
	}
	removed, err = s.ZSetRemoveRangeByRank("z", 0, -1)
	if err != nil || removed != 1 || s.Type("z") != "none" {
		t.Fatalf("final remove=%d type=%q err=%v", removed, s.Type("z"), err)
	}
}

func TestZSetScoreRangeWrongTypeAndMissing(t *testing.T) {
	s := New()
	min := ZSetScoreBound{Score: math.Inf(-1)}
	max := ZSetScoreBound{Score: math.Inf(1)}
	if count, err := s.ZSetCount("missing", min, max); err != nil || count != 0 {
		t.Fatalf("missing count=%d err=%v", count, err)
	}
	if got, err := s.ZSetRangeByScore("missing", min, max, false, 0, -1); err != nil || len(got) != 0 {
		t.Fatalf("missing range=%v err=%v", got, err)
	}
	if err := s.SetWithTTL("plain", []byte("v"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ZSetCount("plain", min, max); err == nil {
		t.Fatal("expected wrong type from count")
	}
	if _, err := s.ZSetRemoveRangeByScore("plain", min, max); err == nil {
		t.Fatal("expected wrong type from remove")
	}
}
