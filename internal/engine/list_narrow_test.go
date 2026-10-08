package engine

import (
	"bytes"
	"math/rand"
	"testing"
)

// Lists under 64 KiB of payload use 16-bit offsets; past it they switch to
// 32-bit offsets, and trimming switches back. A randomized mix of pushes,
// pops and compaction around that boundary must match a slice model.
func TestListOffsetWidthSwitchMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	s := New()
	const key = "narrow"
	var ref [][]byte

	widthOf := func() int {
		sh := s.shardFor(key)
		sh.mu.RLock()
		defer sh.mu.RUnlock()
		e, ok := sh.get(key)
		if !ok || !isIndexedList(sh.encoded(e)) {
			return 0
		}
		return indexedListOffsetWidth(sh.encoded(e))
	}
	check := func(step int) {
		t.Helper()
		got, err := s.ListRange(key, 0, -1)
		if err != nil || len(got) != len(ref) {
			t.Fatalf("step %d: range len=%d want %d err=%v", step, len(got), len(ref), err)
		}
		for i := range ref {
			if !bytes.Equal(got[i], ref[i]) {
				t.Fatalf("step %d: element %d differs", step, i)
			}
		}
		if len(ref) > 0 {
			i := rng.Intn(len(ref))
			v, ok, err := s.ListIndex(key, int64(i))
			if err != nil || !ok || !bytes.Equal(v, ref[i]) {
				t.Fatalf("step %d: LINDEX %d mismatch", step, i)
			}
		}
	}

	sawNarrow, sawWide := false, false
	for step := 0; step < 600; step++ {
		switch r := rng.Intn(10); {
		case r < 6:
			n := 40 + rng.Intn(600)
			v := make([]byte, n)
			rng.Read(v)
			if _, err := s.ListPushRight(key, [][]byte{v}); err != nil {
				t.Fatal(err)
			}
			ref = append(ref, v)
		case r < 7 && len(ref) > 40:
			k := 1 + rng.Intn(5)
			if _, err := s.ListPopLeft(key, k); err != nil {
				t.Fatal(err)
			}
			ref = ref[k:]
		case r < 8 && len(ref) > 40:
			k := 1 + rng.Intn(5)
			if _, err := s.ListPopRight(key, k); err != nil {
				t.Fatal(err)
			}
			ref = ref[:len(ref)-k]
		case r < 9:
			s.Compact(256 << 20)
		default:
			v := []byte("front")
			if _, err := s.ListPushLeft(key, [][]byte{v}); err != nil {
				t.Fatal(err)
			}
			ref = append([][]byte{v}, ref...)
		}
		switch widthOf() {
		case 2:
			sawNarrow = true
		case 4:
			sawWide = true
		}
		if step%25 == 0 {
			check(step)
		}
	}
	check(600)
	if !sawNarrow || !sawWide {
		t.Fatalf("test did not cross the width boundary: narrow=%v wide=%v", sawNarrow, sawWide)
	}
}

// A 100-element list of 64-byte values must be narrow and exactly sized after
// trimming: 15 + 100*2 + 100*65 bytes.
func TestTrimmedMediumListIsNarrowAndTight(t *testing.T) {
	s := New()
	value := bytes.Repeat([]byte{7}, 64)
	for i := 0; i < 100; i++ {
		if _, err := s.ListPushRight("m", [][]byte{value}); err != nil {
			t.Fatal(err)
		}
	}
	s.Compact(256 << 20)
	sh := s.shardFor("m")
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, _ := sh.get("m")
	data := sh.encoded(e)
	if indexedListOffsetWidth(data) != 2 {
		t.Fatal("medium list should use 16-bit offsets")
	}
	if want := 15 + 100*2 + 100*65; len(data) != want {
		t.Fatalf("trimmed size %d, want %d", len(data), want)
	}
}
