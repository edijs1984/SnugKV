package engine

import (
	"math"
	"testing"
	"time"
)

func zitem(score float64, member string) ZSetItem {
	return ZSetItem{Score: score, Member: []byte(member)}
}

func TestZSetCoreOrderingAndLookup(t *testing.T) {
	s := New()
	added, _, _, err := s.ZSetAdd("z", []ZSetItem{
		zitem(2, "b"),
		zitem(1, "c"),
		zitem(1, "a"),
	}, ZSetAddOptions{})
	if err != nil || added != 3 {
		t.Fatalf("add=%d err=%v", added, err)
	}
	if got := s.Type("z"); got != "zset" {
		t.Fatalf("type=%q", got)
	}
	if card, err := s.ZSetCard("z"); err != nil || card != 3 {
		t.Fatalf("card=%d err=%v", card, err)
	}
	items, err := s.ZSetRange("z", 0, -1, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "c", "b"}
	for i := range want {
		if string(items[i].Member) != want[i] {
			t.Fatalf("range[%d]=%q want=%q", i, items[i].Member, want[i])
		}
	}
	if score, found, err := s.ZSetScore("z", []byte("c")); err != nil || !found || score != 1 {
		t.Fatalf("score=%v found=%v err=%v", score, found, err)
	}
	if rank, found, err := s.ZSetRank("z", []byte("c"), false); err != nil || !found || rank != 1 {
		t.Fatalf("rank=%d found=%v err=%v", rank, found, err)
	}
	if rank, found, err := s.ZSetRank("z", []byte("c"), true); err != nil || !found || rank != 1 {
		t.Fatalf("revrank=%d found=%v err=%v", rank, found, err)
	}
	reverse, err := s.ZSetRange("z", 0, 1, true)
	if err != nil || len(reverse) != 2 || string(reverse[0].Member) != "b" || string(reverse[1].Member) != "c" {
		t.Fatalf("reverse=%v err=%v", reverse, err)
	}
}

func TestZSetAddOptions(t *testing.T) {
	s := New()
	if added, _, _, err := s.ZSetAdd("z", []ZSetItem{zitem(1, "a")}, ZSetAddOptions{}); err != nil || added != 1 {
		t.Fatalf("initial add=%d err=%v", added, err)
	}
	if added, _, _, err := s.ZSetAdd("z", []ZSetItem{zitem(2, "a")}, ZSetAddOptions{NX: true}); err != nil || added != 0 {
		t.Fatalf("NX add=%d err=%v", added, err)
	}
	if score, _, _ := s.ZSetScore("z", []byte("a")); score != 1 {
		t.Fatalf("NX changed score=%v", score)
	}
	if added, _, _, err := s.ZSetAdd("z", []ZSetItem{zitem(2, "missing")}, ZSetAddOptions{XX: true}); err != nil || added != 0 {
		t.Fatalf("XX add=%d err=%v", added, err)
	}
	if changed, _, _, err := s.ZSetAdd("z", []ZSetItem{zitem(3, "a")}, ZSetAddOptions{XX: true, CH: true}); err != nil || changed != 1 {
		t.Fatalf("XX CH=%d err=%v", changed, err)
	}
	if changed, _, _, err := s.ZSetAdd("z", []ZSetItem{zitem(2, "a")}, ZSetAddOptions{GT: true, CH: true}); err != nil || changed != 0 {
		t.Fatalf("GT lower changed=%d err=%v", changed, err)
	}
	if changed, _, _, err := s.ZSetAdd("z", []ZSetItem{zitem(4, "a")}, ZSetAddOptions{GT: true, CH: true}); err != nil || changed != 1 {
		t.Fatalf("GT higher changed=%d err=%v", changed, err)
	}
	if changed, _, _, err := s.ZSetAdd("z", []ZSetItem{zitem(5, "a")}, ZSetAddOptions{LT: true, CH: true}); err != nil || changed != 0 {
		t.Fatalf("LT higher changed=%d err=%v", changed, err)
	}
	if changed, _, _, err := s.ZSetAdd("z", []ZSetItem{zitem(2, "a")}, ZSetAddOptions{LT: true, CH: true}); err != nil || changed != 1 {
		t.Fatalf("LT lower changed=%d err=%v", changed, err)
	}

	_, incremented, score, err := s.ZSetAdd("z", []ZSetItem{zitem(1.5, "a")}, ZSetAddOptions{INCR: true})
	if err != nil || !incremented || score != 3.5 {
		t.Fatalf("INCR score=%v applied=%v err=%v", score, incremented, err)
	}
	_, incremented, _, err = s.ZSetAdd("z", []ZSetItem{zitem(1, "a")}, ZSetAddOptions{NX: true, INCR: true})
	if err != nil || incremented {
		t.Fatalf("NX INCR applied=%v err=%v", incremented, err)
	}
}

func TestZSetMutationPreservesTTLAndDeleteLast(t *testing.T) {
	s := New()
	if _, _, _, err := s.ZSetAdd("z", []ZSetItem{zitem(1, "a"), zitem(2, "b")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if !s.Expire("z", time.Minute) {
		t.Fatal("expire failed")
	}
	if _, _, _, err := s.ZSetAdd("z", []ZSetItem{zitem(3, "a")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if ttl := s.TTL("z", true); ttl <= 0 {
		t.Fatalf("ttl lost: %d", ttl)
	}
	if removed, err := s.ZSetRemove("z", [][]byte{[]byte("a")}); err != nil || removed != 1 {
		t.Fatalf("remove=%d err=%v", removed, err)
	}
	if ttl := s.TTL("z", true); ttl <= 0 {
		t.Fatalf("ttl lost after remove: %d", ttl)
	}
	if removed, err := s.ZSetRemove("z", [][]byte{[]byte("b")}); err != nil || removed != 1 {
		t.Fatalf("final remove=%d err=%v", removed, err)
	}
	if got := s.Type("z"); got != "none" {
		t.Fatalf("type after final remove=%q", got)
	}
}

func TestZSetWrongTypePersistenceAndOptimizerExclusion(t *testing.T) {
	s, err := NewWithOptions(Options{Shards: 16, Encoding: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetWithTTL("plain", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.ZSetAdd("plain", []ZSetItem{zitem(1, "a")}, ZSetAddOptions{}); err == nil {
		t.Fatal("expected wrong type")
	}
	if _, _, _, err := s.ZSetAdd("z", []ZSetItem{zitem(math.Inf(-1), "low"), zitem(1, "mid"), zitem(math.Inf(1), "high")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, eligible := s.OptimizationEligible("z", 0, 0); eligible {
		t.Fatal("ZSET must be excluded from generic optimizer")
	}
	records := s.Export([]string{"z"})
	if len(records) != 1 || ValueType(records[0].ValueType) != TypeZSet {
		t.Fatalf("records=%v", records)
	}
	restored, _ := NewWithShards(16)
	if err := restored.Restore(records, false); err != nil {
		t.Fatal(err)
	}
	items, err := restored.ZSetRange("z", 0, -1, false)
	if err != nil || len(items) != 3 || string(items[0].Member) != "low" || string(items[2].Member) != "high" {
		t.Fatalf("restored=%v err=%v", items, err)
	}
}

func TestZSetStorageStats(t *testing.T) {
	s := New()
	if _, _, _, err := s.ZSetAdd("z", []ZSetItem{zitem(1, "aaaaaaaaaaaaaaaa"), zitem(2, "bbbbbbbbbbbbbbbb")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	stats, found, err := s.ZSetStorageStats("z")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if stats.Members != 2 || stats.MemberBytes != 32 || stats.PackedBytes != 54 || stats.StoredBytes != 54 || stats.Encoding != "packed" {
		t.Fatalf("stats=%+v", stats)
	}
}
