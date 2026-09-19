package engine

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestListMoveAcrossKeysPreservesOrderAndTTL(t *testing.T) {
	s := New()
	if _, err := s.ListPushRight("source", [][]byte{[]byte("a"), []byte("b"), []byte("c")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListPushRight("dest", [][]byte{[]byte("x"), []byte("y")}); err != nil {
		t.Fatal(err)
	}
	if !s.Expire("source", time.Minute) || !s.Expire("dest", 2*time.Minute) {
		t.Fatal("failed to set TTLs")
	}

	moved, found, err := s.ListMove("source", "dest", false, true)
	if err != nil || !found || string(moved) != "c" {
		t.Fatalf("RIGHT->LEFT moved=%q found=%v err=%v", moved, found, err)
	}
	assertListElements(t, mustListRange(t, s, "source", 0, -1), "a", "b")
	assertListElements(t, mustListRange(t, s, "dest", 0, -1), "c", "x", "y")
	if ttl := s.TTL("source", true); ttl <= 0 {
		t.Fatalf("source TTL lost: %d", ttl)
	}
	if ttl := s.TTL("dest", true); ttl <= 0 {
		t.Fatalf("destination TTL lost: %d", ttl)
	}

	moved, found, err = s.ListMove("source", "dest", true, false)
	if err != nil || !found || string(moved) != "a" {
		t.Fatalf("LEFT->RIGHT moved=%q found=%v err=%v", moved, found, err)
	}
	assertListElements(t, mustListRange(t, s, "source", 0, -1), "b")
	assertListElements(t, mustListRange(t, s, "dest", 0, -1), "c", "x", "y", "a")

	moved, found, err = s.ListMove("source", "new-dest", false, false)
	if err != nil || !found || string(moved) != "b" {
		t.Fatalf("final move=%q found=%v err=%v", moved, found, err)
	}
	if got := s.Type("source"); got != "none" {
		t.Fatalf("source after final move type=%q", got)
	}
	assertListElements(t, mustListRange(t, s, "new-dest", 0, -1), "b")
	if ttl := s.TTL("new-dest", true); ttl != -1 {
		t.Fatalf("new destination unexpectedly inherited TTL: %d", ttl)
	}
}

func TestListMoveSameKeyRotationAndNoop(t *testing.T) {
	s := New()
	if _, err := s.ListPushRight("list", [][]byte{[]byte("a"), []byte("b"), []byte("c")}); err != nil {
		t.Fatal(err)
	}
	if !s.Expire("list", time.Minute) {
		t.Fatal("failed to set TTL")
	}

	moved, found, err := s.ListMove("list", "list", false, true)
	if err != nil || !found || string(moved) != "c" {
		t.Fatalf("RIGHT->LEFT moved=%q found=%v err=%v", moved, found, err)
	}
	assertListElements(t, mustListRange(t, s, "list", 0, -1), "c", "a", "b")

	moved, found, err = s.ListMove("list", "list", true, false)
	if err != nil || !found || string(moved) != "c" {
		t.Fatalf("LEFT->RIGHT moved=%q found=%v err=%v", moved, found, err)
	}
	assertListElements(t, mustListRange(t, s, "list", 0, -1), "a", "b", "c")

	moved, found, err = s.ListMove("list", "list", true, true)
	if err != nil || !found || string(moved) != "a" {
		t.Fatalf("LEFT->LEFT moved=%q found=%v err=%v", moved, found, err)
	}
	assertListElements(t, mustListRange(t, s, "list", 0, -1), "a", "b", "c")
	if ttl := s.TTL("list", true); ttl <= 0 {
		t.Fatalf("same-key move lost TTL: %d", ttl)
	}
}

func TestListMoveMissingAndWrongType(t *testing.T) {
	s := New()
	if err := s.Set("plain", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}

	// Redis returns nil without inspecting destination type when source is absent.
	value, found, err := s.ListMove("missing", "plain", false, true)
	if err != nil || found || value != nil {
		t.Fatalf("missing source value=%q found=%v err=%v", value, found, err)
	}

	if _, err := s.ListPushRight("source", [][]byte{[]byte("a"), []byte("b")}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ListMove("source", "plain", false, true); err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") {
		t.Fatalf("destination wrongtype err=%v", err)
	}
	assertListElements(t, mustListRange(t, s, "source", 0, -1), "a", "b")

	if _, _, err := s.ListMove("plain", "source", false, true); err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") {
		t.Fatalf("source wrongtype err=%v", err)
	}
}

func TestListMoveOOMIsAtomic(t *testing.T) {
	s, err := NewWithOptions(Options{Shards: 1})
	if err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("x"), 20_000)
	if _, err := s.ListPushRight("source", [][]byte{big, []byte("tail")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListPushRight("dest", [][]byte{[]byte("d")}); err != nil {
		t.Fatal(err)
	}
	beforeSource := mustListRange(t, s, "source", 0, -1)
	beforeDest := mustListRange(t, s, "dest", 0, -1)
	beforeMemory := s.Memory()

	s.memory.mu.Lock()
	s.memory.max.Store(s.memory.used)
	s.memory.mu.Unlock()

	if _, _, err := s.ListMove("source", "dest", true, false); err != ErrOOM {
		t.Fatalf("move err=%v want ErrOOM", err)
	}
	assertListElementsBytes(t, mustListRange(t, s, "source", 0, -1), beforeSource)
	assertListElementsBytes(t, mustListRange(t, s, "dest", 0, -1), beforeDest)
	if after := s.Memory(); after.AccountedBytes != beforeMemory.AccountedBytes {
		t.Fatalf("failed move changed accounted memory: before=%d after=%d", beforeMemory.AccountedBytes, after.AccountedBytes)
	}
}

func assertListElementsBytes(t *testing.T, got, want [][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len=%d want=%d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("element[%d] changed", i)
		}
	}
}
