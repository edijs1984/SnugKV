package engine

import (
	"bytes"
	"testing"
	"time"
)

func TestZSetPopMinMaxPreservesTTLAndDeletesEmpty(t *testing.T) {
	s := New()
	if _, _, _, err := s.ZSetAdd("z", []ZSetItem{zitem(1, "a"), zitem(2, "b"), zitem(3, "c")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if !s.Expire("z", time.Minute) {
		t.Fatal("expire failed")
	}
	items, err := s.ZSetPop("z", 1, false)
	if err != nil || len(items) != 1 || string(items[0].Member) != "a" || items[0].Score != 1 {
		t.Fatalf("min=%v err=%v", items, err)
	}
	if ttl := s.TTL("z", true); ttl <= 0 {
		t.Fatalf("ttl lost: %d", ttl)
	}
	items, err = s.ZSetPop("z", 2, true)
	if err != nil || len(items) != 2 || string(items[0].Member) != "c" || string(items[1].Member) != "b" {
		t.Fatalf("max=%v err=%v", items, err)
	}
	if got := s.Type("z"); got != "none" {
		t.Fatalf("type=%q", got)
	}
}

func TestZSetMPopUsesFirstNonEmptyKey(t *testing.T) {
	s := New()
	if _, _, _, err := s.ZSetAdd("second", []ZSetItem{zitem(1, "a"), zitem(2, "b")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	key, items, found, err := s.ZSetMPop([]string{"missing", "second"}, 1, true)
	if err != nil || !found || key != "second" || len(items) != 1 || string(items[0].Member) != "b" {
		t.Fatalf("key=%q items=%v found=%v err=%v", key, items, found, err)
	}
	remaining, err := s.ZSetRange("second", 0, -1, false)
	if err != nil || len(remaining) != 1 || string(remaining[0].Member) != "a" {
		t.Fatalf("remaining=%v err=%v", remaining, err)
	}
}

func TestZSetScoresRandomAndScan(t *testing.T) {
	s := New()
	if _, _, _, err := s.ZSetAdd("z", []ZSetItem{zitem(1, "alpha"), zitem(2, "beta"), zitem(3, "gamma")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	scores, found, err := s.ZSetScores("z", [][]byte{[]byte("beta"), []byte("missing"), []byte("alpha")})
	if err != nil || !found[0] || found[1] || !found[2] || scores[0] != 2 || scores[2] != 1 {
		t.Fatalf("scores=%v found=%v err=%v", scores, found, err)
	}
	random, err := s.ZSetRandomMembers("z", 2)
	if err != nil || len(random) != 2 || bytes.Equal(random[0].Member, random[1].Member) {
		t.Fatalf("random=%v err=%v", random, err)
	}
	next, page, err := s.ZSetScan("z", 0, 1, "*a*")
	if err != nil || len(page) != 1 || next == 0 {
		t.Fatalf("page=%v next=%d err=%v", page, next, err)
	}
	_, page2, err := s.ZSetScan("z", next, 10, "*a*")
	if err != nil || len(page2) == 0 {
		t.Fatalf("page2=%v err=%v", page2, err)
	}
}

func TestZSetRangeStoreRankScoreAndDestinationAsSource(t *testing.T) {
	s := New()
	if _, _, _, err := s.ZSetAdd("src", []ZSetItem{zitem(1, "a"), zitem(2, "b"), zitem(3, "c"), zitem(4, "d")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.ZSetAdd("dest", []ZSetItem{zitem(99, "old")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if !s.Expire("dest", time.Minute) {
		t.Fatal("expire failed")
	}
	count, err := s.ZSetRangeStoreByRank("dest", "src", 1, 2, false)
	if err != nil || count != 2 {
		t.Fatalf("rank count=%d err=%v", count, err)
	}
	if ttl := s.TTL("dest", true); ttl != -1 {
		t.Fatalf("ttl=%d want -1", ttl)
	}
	items, _ := s.ZSetRange("dest", 0, -1, false)
	assertZSetItems(t, items, []ZSetItem{zitem(2, "b"), zitem(3, "c")})
	count, err = s.ZSetRangeStoreByScore("src", "src", ZSetScoreBound{Score: 2}, ZSetScoreBound{Score: 4}, true, 1, 2)
	if err != nil || count != 2 {
		t.Fatalf("score count=%d err=%v", count, err)
	}
	items, _ = s.ZSetRange("src", 0, -1, false)
	assertZSetItems(t, items, []ZSetItem{zitem(2, "b"), zitem(3, "c")})
}

func TestZSetRangeStoreOOMLeavesDestinationUntouched(t *testing.T) {
	s, err := NewWithOptions(Options{Shards: 1})
	if err != nil {
		t.Fatal(err)
	}
	bigA := bytes.Repeat([]byte("a"), 6000)
	bigB := bytes.Repeat([]byte("b"), 6000)
	if _, _, _, err := s.ZSetAdd("src", []ZSetItem{{Member: bigA, Score: 1}, {Member: bigB, Score: 2}}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.ZSetAdd("dest", []ZSetItem{zitem(9, "old")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	before := s.Memory()
	s.memory.mu.Lock()
	max := s.memory.used
	s.memory.mu.Unlock()
	s.memory.max.Store(max)
	if _, err := s.ZSetRangeStoreByRank("dest", "src", 0, -1, false); err != ErrOOM {
		t.Fatalf("err=%v want OOM", err)
	}
	items, err := s.ZSetRange("dest", 0, -1, false)
	if err != nil || len(items) != 1 || string(items[0].Member) != "old" {
		t.Fatalf("dest=%v err=%v", items, err)
	}
	if after := s.Memory(); after.AccountedBytes != before.AccountedBytes {
		t.Fatalf("memory changed before=%d after=%d", before.AccountedBytes, after.AccountedBytes)
	}
}
