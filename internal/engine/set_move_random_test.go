package engine

import (
	"strings"
	"testing"
	"time"
)

func TestSetMoveAtomicSemanticsAndTTL(t *testing.T) {
	s := New()
	mustSetAdd(t, s, "source", "a", "b")
	mustSetAdd(t, s, "dest", "b", "c")
	if !s.Expire("source", time.Minute) || !s.Expire("dest", 2*time.Minute) {
		t.Fatal("failed to set TTLs")
	}

	moved, err := s.SetMove("source", "dest", []byte("a"))
	if err != nil || !moved {
		t.Fatalf("move a=%v err=%v", moved, err)
	}
	source, err := s.SetMembers("source")
	if err != nil {
		t.Fatal(err)
	}
	assertSetMembersEqual(t, source, "b")
	dest, err := s.SetMembers("dest")
	if err != nil {
		t.Fatal(err)
	}
	assertSetMembersEqual(t, dest, "a", "b", "c")
	if ttl := s.TTL("source", true); ttl <= 0 {
		t.Fatalf("source TTL lost: %d", ttl)
	}
	if ttl := s.TTL("dest", true); ttl <= 0 {
		t.Fatalf("destination TTL lost: %d", ttl)
	}

	// b already exists in destination, so SMOVE only removes it from source.
	moved, err = s.SetMove("source", "dest", []byte("b"))
	if err != nil || !moved {
		t.Fatalf("move existing destination member=%v err=%v", moved, err)
	}
	if got := s.Type("source"); got != "none" {
		t.Fatalf("empty source type=%q", got)
	}
	dest, err = s.SetMembers("dest")
	if err != nil {
		t.Fatal(err)
	}
	assertSetMembersEqual(t, dest, "a", "b", "c")
}

func TestSetMoveSameKeyMissingAndWrongType(t *testing.T) {
	s := New()
	mustSetAdd(t, s, "same", "x")

	moved, err := s.SetMove("same", "same", []byte("x"))
	if err != nil || !moved {
		t.Fatalf("same-key existing move=%v err=%v", moved, err)
	}
	members, err := s.SetMembers("same")
	if err != nil {
		t.Fatal(err)
	}
	assertSetMembersEqual(t, members, "x")

	moved, err = s.SetMove("same", "same", []byte("missing"))
	if err != nil || moved {
		t.Fatalf("same-key missing move=%v err=%v", moved, err)
	}

	if err := s.Set("plain", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}
	// Redis returns 0 without inspecting destination type when source is absent.
	moved, err = s.SetMove("missing", "plain", []byte("x"))
	if err != nil || moved {
		t.Fatalf("missing source move=%v err=%v", moved, err)
	}

	mustSetAdd(t, s, "source", "x")
	if _, err := s.SetMove("source", "plain", []byte("missing")); err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") {
		t.Fatalf("destination wrongtype err=%v", err)
	}
}

func TestSetRandomMembersSemantics(t *testing.T) {
	s := New()
	mustSetAdd(t, s, "set", "a", "b", "c", "d")

	distinct, err := s.SetRandomMembers("set", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(distinct) != 3 {
		t.Fatalf("distinct len=%d want 3", len(distinct))
	}
	seen := make(map[string]bool)
	for _, member := range distinct {
		if seen[string(member)] {
			t.Fatalf("duplicate in positive-count result: %q", member)
		}
		seen[string(member)] = true
		found, err := s.SetContains("set", member)
		if err != nil || !found {
			t.Fatalf("random member %q not in set err=%v", member, err)
		}
	}

	all, err := s.SetRandomMembers("set", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("oversized positive count len=%d want 4", len(all))
	}

	withReplacement, err := s.SetRandomMembers("set", -9)
	if err != nil {
		t.Fatal(err)
	}
	if len(withReplacement) != 9 {
		t.Fatalf("negative count len=%d want 9", len(withReplacement))
	}
	for _, member := range withReplacement {
		found, err := s.SetContains("set", member)
		if err != nil || !found {
			t.Fatalf("replacement member %q not in set err=%v", member, err)
		}
	}

	members, err := s.SetMembers("set")
	if err != nil {
		t.Fatal(err)
	}
	assertSetMembersEqual(t, members, "a", "b", "c", "d")
}

func TestSetPopRemovesDistinctMembersAndPreservesTTL(t *testing.T) {
	s := New()
	mustSetAdd(t, s, "set", "a", "b", "c", "d")
	if !s.Expire("set", time.Minute) {
		t.Fatal("Expire returned false")
	}

	popped, err := s.SetPop("set", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(popped) != 2 || string(popped[0]) == string(popped[1]) {
		t.Fatalf("popped=%q", popped)
	}
	if ttl := s.TTL("set", true); ttl <= 0 {
		t.Fatalf("TTL lost after partial SPOP: %d", ttl)
	}
	if length, err := s.SetLen("set"); err != nil || length != 2 {
		t.Fatalf("remaining length=%d err=%v", length, err)
	}
	for _, member := range popped {
		found, err := s.SetContains("set", member)
		if err != nil || found {
			t.Fatalf("popped member %q still present=%v err=%v", member, found, err)
		}
	}

	last, err := s.SetPop("set", 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(last) != 2 {
		t.Fatalf("final pop len=%d want 2", len(last))
	}
	if got := s.Type("set"); got != "none" {
		t.Fatalf("type after popping all=%q", got)
	}
}
