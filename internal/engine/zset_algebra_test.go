package engine

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestZSetAlgebraUnionWeightsAndAggregates(t *testing.T) {
	s := New()
	if _, _, _, err := s.ZSetAdd("a", []ZSetItem{zitem(1, "one"), zitem(2, "two")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.ZSetAdd("b", []ZSetItem{zitem(3, "one"), zitem(4, "three")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}

	got, err := s.ZSetUnion([]string{"a", "b"}, []float64{2, 3}, ZSetAggregateSum)
	if err != nil {
		t.Fatal(err)
	}
	assertZSetItems(t, got, []ZSetItem{zitem(4, "two"), zitem(11, "one"), zitem(12, "three")})

	got, err = s.ZSetUnion([]string{"a", "b"}, nil, ZSetAggregateMin)
	if err != nil {
		t.Fatal(err)
	}
	assertZSetItems(t, got, []ZSetItem{zitem(1, "one"), zitem(2, "two"), zitem(4, "three")})

	got, err = s.ZSetUnion([]string{"a", "b"}, nil, ZSetAggregateMax)
	if err != nil {
		t.Fatal(err)
	}
	assertZSetItems(t, got, []ZSetItem{zitem(2, "two"), zitem(3, "one"), zitem(4, "three")})

	got, err = s.ZSetUnion([]string{"a", "b"}, []float64{5, 7}, ZSetAggregateCount)
	if err != nil {
		t.Fatal(err)
	}
	assertZSetItems(t, got, []ZSetItem{zitem(5, "two"), zitem(7, "three"), zitem(12, "one")})
}

func TestZSetAlgebraIntersectionDiffAndSetSources(t *testing.T) {
	s := New()
	if _, _, _, err := s.ZSetAdd("a", []ZSetItem{zitem(1, "a"), zitem(2, "b"), zitem(3, "c")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.ZSetAdd("b", []ZSetItem{zitem(10, "b"), zitem(20, "c"), zitem(30, "d")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAdd("plain", [][]byte{[]byte("b"), []byte("c"), []byte("x")}); err != nil {
		t.Fatal(err)
	}

	inter, err := s.ZSetIntersect([]string{"a", "b", "plain"}, nil, ZSetAggregateSum)
	if err != nil {
		t.Fatal(err)
	}
	assertZSetItems(t, inter, []ZSetItem{zitem(13, "b"), zitem(24, "c")})

	diff, err := s.ZSetDiff([]string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	assertZSetItems(t, diff, []ZSetItem{zitem(1, "a")})

	union, err := s.ZSetUnion([]string{"plain"}, nil, ZSetAggregateSum)
	if err != nil {
		t.Fatal(err)
	}
	assertZSetItems(t, union, []ZSetItem{zitem(1, "b"), zitem(1, "c"), zitem(1, "x")})
}

func TestZSetAlgebraMissingWrongTypeAndCardinalityLimit(t *testing.T) {
	s := New()
	if _, _, _, err := s.ZSetAdd("a", []ZSetItem{zitem(1, "a"), zitem(2, "b"), zitem(3, "c")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.ZSetAdd("b", []ZSetItem{zitem(4, "a"), zitem(5, "b"), zitem(6, "x")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("bad", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}

	inter, err := s.ZSetIntersect([]string{"a", "missing"}, nil, ZSetAggregateSum)
	if err != nil || len(inter) != 0 {
		t.Fatalf("missing intersection=%v err=%v", inter, err)
	}
	if _, err := s.ZSetUnion([]string{"a", "bad"}, nil, ZSetAggregateSum); err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") {
		t.Fatalf("wrong type err=%v", err)
	}
	if count, err := s.ZSetIntersectCardinality([]string{"a", "b"}, 1); err != nil || count != 1 {
		t.Fatalf("limited cardinality=%d err=%v", count, err)
	}
	if count, err := s.ZSetIntersectCardinality([]string{"a", "b"}, 0); err != nil || count != 2 {
		t.Fatalf("cardinality=%d err=%v", count, err)
	}
}

func TestZSetAlgebraStoreDestinationAsSourceAndTTL(t *testing.T) {
	s := New()
	if _, _, _, err := s.ZSetAdd("dest", []ZSetItem{zitem(1, "a"), zitem(2, "b")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.ZSetAdd("other", []ZSetItem{zitem(3, "b"), zitem(4, "c")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if !s.Expire("dest", time.Minute) {
		t.Fatal("expire failed")
	}
	count, err := s.ZSetUnionStore("dest", []string{"dest", "other"}, nil, ZSetAggregateSum)
	if err != nil || count != 3 {
		t.Fatalf("store count=%d err=%v", count, err)
	}
	if ttl := s.TTL("dest", true); ttl != -1 {
		t.Fatalf("destination ttl=%d want -1", ttl)
	}
	items, err := s.ZSetRange("dest", 0, -1, false)
	if err != nil {
		t.Fatal(err)
	}
	assertZSetItems(t, items, []ZSetItem{zitem(1, "a"), zitem(4, "c"), zitem(5, "b")})
}

func TestZSetAlgebraStoreEmptyDeletesAndWrongTypeIsAtomic(t *testing.T) {
	s := New()
	if _, _, _, err := s.ZSetAdd("dest", []ZSetItem{zitem(9, "old")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.ZSetAdd("good", []ZSetItem{zitem(1, "a")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("bad", []byte("string"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ZSetUnionStore("dest", []string{"good", "bad"}, nil, ZSetAggregateSum); err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") {
		t.Fatalf("wrong type err=%v", err)
	}
	items, err := s.ZSetRange("dest", 0, -1, false)
	if err != nil {
		t.Fatal(err)
	}
	assertZSetItems(t, items, []ZSetItem{zitem(9, "old")})

	count, err := s.ZSetIntersectStore("dest", []string{"good", "missing"}, nil, ZSetAggregateSum)
	if err != nil || count != 0 {
		t.Fatalf("empty store count=%d err=%v", count, err)
	}
	if got := s.Type("dest"); got != "none" {
		t.Fatalf("destination type=%q", got)
	}
}

func TestZSetAlgebraNaNCombinationNormalizesToZero(t *testing.T) {
	s := New()
	if _, _, _, err := s.ZSetAdd("a", []ZSetItem{zitem(math.Inf(1), "x")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.ZSetAdd("b", []ZSetItem{zitem(math.Inf(-1), "x")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	items, err := s.ZSetUnion([]string{"a", "b"}, nil, ZSetAggregateSum)
	if err != nil {
		t.Fatal(err)
	}
	assertZSetItems(t, items, []ZSetItem{zitem(0, "x")})
}

func assertZSetItems(t *testing.T, got, want []ZSetItem) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len=%d want=%d got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Score != want[i].Score || string(got[i].Member) != string(want[i].Member) {
			t.Fatalf("item[%d]=%q/%v want=%q/%v", i, got[i].Member, got[i].Score, want[i].Member, want[i].Score)
		}
	}
}
