package engine

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestPackedListRoundTripPreservesOrderDuplicatesAndBinary(t *testing.T) {
	input := [][]byte{[]byte("b"), []byte("a"), []byte("a"), {0xff, 0x00, 0x01}}
	packed, err := encodePackedList(input)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodePackedList(packed)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(input) {
		t.Fatalf("len=%d want %d", len(got), len(input))
	}
	for i := range input {
		if !bytes.Equal(got[i], input[i]) {
			t.Fatalf("element[%d]=%v want %v", i, got[i], input[i])
		}
	}
}

func TestListPushPopOrderingAndTTL(t *testing.T) {
	s := New()
	if n, err := s.ListPushRight("list", [][]byte{[]byte("a"), []byte("b")}); err != nil || n != 2 {
		t.Fatalf("RPUSH n=%d err=%v", n, err)
	}
	if !s.Expire("list", time.Minute) {
		t.Fatal("expire failed")
	}
	if n, err := s.ListPushLeft("list", [][]byte{[]byte("c"), []byte("d")}); err != nil || n != 4 {
		t.Fatalf("LPUSH n=%d err=%v", n, err)
	}
	assertListElements(t, mustListRange(t, s, "list", 0, -1), "d", "c", "a", "b")
	if ttl := s.TTL("list", true); ttl <= 0 {
		t.Fatalf("TTL after push=%d", ttl)
	}

	popped, err := s.ListPopRight("list", 2)
	if err != nil {
		t.Fatal(err)
	}
	assertListElements(t, popped, "b", "a")
	assertListElements(t, mustListRange(t, s, "list", 0, -1), "d", "c")
	if ttl := s.TTL("list", true); ttl <= 0 {
		t.Fatalf("TTL after pop=%d", ttl)
	}

	popped, err = s.ListPopLeft("list", 10)
	if err != nil {
		t.Fatal(err)
	}
	assertListElements(t, popped, "d", "c")
	if got := s.Type("list"); got != "none" {
		t.Fatalf("type after final pop=%q", got)
	}
}

func TestListIndexAndRange(t *testing.T) {
	s := New()
	if _, err := s.ListPushRight("list", [][]byte{[]byte("a"), []byte("b"), []byte("c"), []byte("d")}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		index int64
		want  string
		ok    bool
	}{
		{0, "a", true}, {2, "c", true}, {-1, "d", true}, {-4, "a", true}, {4, "", false}, {-5, "", false},
	} {
		got, ok, err := s.ListIndex("list", tc.index)
		if err != nil || ok != tc.ok || tc.ok && string(got) != tc.want {
			t.Fatalf("LINDEX %d got=%q ok=%v err=%v", tc.index, got, ok, err)
		}
	}

	assertListElements(t, mustListRange(t, s, "list", 1, 2), "b", "c")
	assertListElements(t, mustListRange(t, s, "list", -3, -1), "b", "c", "d")
	assertListElements(t, mustListRange(t, s, "list", -100, 100), "a", "b", "c", "d")
	assertListElements(t, mustListRange(t, s, "list", 3, 1))
}

func TestListWrongTypeAndZeroCount(t *testing.T) {
	s := New()
	if err := s.Set("plain", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}
	checks := []func() error{
		func() error { _, err := s.ListPushLeft("plain", [][]byte{[]byte("x")}); return err },
		func() error { _, err := s.ListPopLeft("plain", 0); return err },
		func() error { _, err := s.ListLen("plain"); return err },
		func() error { _, _, err := s.ListIndex("plain", 0); return err },
		func() error { _, err := s.ListRange("plain", 0, -1); return err },
	}
	for i, check := range checks {
		if err := check(); err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") {
			t.Fatalf("check %d err=%v", i, err)
		}
	}
}

func TestListStorageStatsNoMetadata(t *testing.T) {
	s := New()
	if _, err := s.ListPushRight("list", [][]byte{[]byte("aa"), []byte("bbb")}); err != nil {
		t.Fatal(err)
	}
	stats, ok, err := s.ListStorageStats("list")
	if err != nil || !ok {
		t.Fatalf("stats ok=%v err=%v", ok, err)
	}
	if stats.Elements != 2 || stats.ElementBytes != 5 || stats.Encoding != "packed" || stats.StoredBytes != stats.PackedBytes {
		t.Fatalf("stats=%+v", stats)
	}
	sh := s.shardFor("list")
	sh.mu.RLock()
	e, ok := sh.get("list")
	sh.mu.RUnlock()
	if !ok || e.entryMeta != nil {
		t.Fatalf("LIST metadata sidecar=%v", e.entryMeta)
	}
}

func mustListRange(t *testing.T, s *Store, key string, start, stop int64) [][]byte {
	t.Helper()
	got, err := s.ListRange(key, start, stop)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func assertListElements(t *testing.T, got [][]byte, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len=%d want=%d got=%q", len(got), len(want), got)
	}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Fatalf("element[%d]=%q want=%q", i, got[i], want[i])
		}
	}
}
