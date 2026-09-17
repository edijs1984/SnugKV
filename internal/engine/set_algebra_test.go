package engine

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestSetAlgebraSortedResults(t *testing.T) {
	s := New()
	mustSetAdd(t, s, "a", "a", "b", "d")
	mustSetAdd(t, s, "b", "b", "c", "d")
	mustSetAdd(t, s, "c", "b", "d", "e")

	assertSetMembersEqual(t, mustSetUnion(t, s, []string{"a", "b", "c"}), "a", "b", "c", "d", "e")
	assertSetMembersEqual(t, mustSetIntersect(t, s, []string{"a", "b", "c"}), "b", "d")
	assertSetMembersEqual(t, mustSetDiff(t, s, []string{"a", "b", "c"}), "a")
}

func TestSetAlgebraMissingAndWrongType(t *testing.T) {
	s := New()
	mustSetAdd(t, s, "a", "a", "b")
	if err := s.Set("plain", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}

	assertSetMembersEqual(t, mustSetUnion(t, s, []string{"missing", "a"}), "a", "b")
	assertSetMembersEqual(t, mustSetIntersect(t, s, []string{"a", "missing"}))
	assertSetMembersEqual(t, mustSetDiff(t, s, []string{"a", "missing"}), "a", "b")

	for name, fn := range map[string]func() error{
		"union":     func() error { _, err := s.SetUnion([]string{"a", "plain"}); return err },
		"intersect": func() error { _, err := s.SetIntersect([]string{"missing", "plain"}); return err },
		"diff":      func() error { _, err := s.SetDiff([]string{"missing", "plain"}); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := fn(); err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestSetAlgebraStoreReplacesDestinationAndClearsTTL(t *testing.T) {
	s := New()
	mustSetAdd(t, s, "dest", "a", "b")
	mustSetAdd(t, s, "other", "b", "c")
	if !s.Expire("dest", time.Minute) {
		t.Fatal("expire dest returned false")
	}

	count, err := s.SetUnionStore("dest", []string{"dest", "other"})
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("count=%d want 3", count)
	}
	members, err := s.SetMembers("dest")
	if err != nil {
		t.Fatal(err)
	}
	assertSetMembersEqual(t, members, "a", "b", "c")
	if ttl := s.TTL("dest", true); ttl != -1 {
		t.Fatalf("destination TTL=%d want -1", ttl)
	}
}

func TestSetAlgebraStoreEmptyDeletesDestination(t *testing.T) {
	s := New()
	mustSetAdd(t, s, "source", "a")
	if err := s.Set("dest", []byte("old string"), 0); err != nil {
		t.Fatal(err)
	}

	count, err := s.SetIntersectStore("dest", []string{"source", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("count=%d want 0", count)
	}
	if got := s.Type("dest"); got != "none" {
		t.Fatalf("destination type=%q want none", got)
	}
}

func TestSetAlgebraStoreWrongTypeLeavesDestinationUntouched(t *testing.T) {
	s := New()
	mustSetAdd(t, s, "dest", "old")
	mustSetAdd(t, s, "good", "new")
	if err := s.Set("bad", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}

	if _, err := s.SetUnionStore("dest", []string{"good", "bad"}); err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") {
		t.Fatalf("err=%v", err)
	}
	members, err := s.SetMembers("dest")
	if err != nil {
		t.Fatal(err)
	}
	assertSetMembersEqual(t, members, "old")
}

func TestSetAlgebraStoreAdaptiveEncoding(t *testing.T) {
	s := New()
	mustSetAdd(t, s, "left", "member:00000001", "member:00000002", "member:00000003")
	mustSetAdd(t, s, "right", "member:00000003", "member:00000004")
	if _, err := s.SetUnionStore("dest", []string{"left", "right"}); err != nil {
		t.Fatal(err)
	}
	stats, ok, err := s.SetStorageStats("dest")
	if err != nil || !ok {
		t.Fatalf("stats ok=%v err=%v", ok, err)
	}
	if stats.Encoding != "prefix" {
		t.Fatalf("encoding=%q want prefix", stats.Encoding)
	}
}

func mustSetAdd(t *testing.T, s *Store, key string, members ...string) {
	t.Helper()
	values := make([][]byte, len(members))
	for i := range members {
		values[i] = []byte(members[i])
	}
	if _, err := s.SetAdd(key, values); err != nil {
		t.Fatal(err)
	}
}

func mustSetUnion(t *testing.T, s *Store, keys []string) [][]byte {
	t.Helper()
	members, err := s.SetUnion(keys)
	if err != nil {
		t.Fatal(err)
	}
	return members
}

func mustSetIntersect(t *testing.T, s *Store, keys []string) [][]byte {
	t.Helper()
	members, err := s.SetIntersect(keys)
	if err != nil {
		t.Fatal(err)
	}
	return members
}

func mustSetDiff(t *testing.T, s *Store, keys []string) [][]byte {
	t.Helper()
	members, err := s.SetDiff(keys)
	if err != nil {
		t.Fatal(err)
	}
	return members
}

func assertSetMembersEqual(t *testing.T, got [][]byte, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d (%q)", len(got), len(want), got)
	}
	for i := range want {
		if !bytes.Equal(got[i], []byte(want[i])) {
			t.Fatalf("member[%d]=%q want %q", i, got[i], want[i])
		}
	}
}
