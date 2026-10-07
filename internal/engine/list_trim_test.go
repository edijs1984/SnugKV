package engine

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"
)

func TestTrimIndexedListKeepsElementsAndShrinks(t *testing.T) {
	var elements [][]byte
	for i := 0; i < 1000; i++ {
		elements = append(elements, bytes.Repeat([]byte{byte(i)}, 64))
	}
	data, err := encodeIndexedList(elements)
	if err != nil {
		t.Fatal(err)
	}
	trimmed, ok := trimIndexedList(data)
	if !ok {
		t.Fatal("expected trim to shrink a freshly encoded list")
	}
	if len(trimmed) >= len(data) {
		t.Fatalf("trimmed %d not smaller than %d", len(trimmed), len(data))
	}
	count, capacity, used, start, err := indexedListMeta(trimmed)
	if err != nil || count != 1000 || capacity != 1000 || start+used != len(trimmed) {
		t.Fatalf("meta count=%d capacity=%d used=%d len=%d err=%v", count, capacity, used, len(trimmed), err)
	}
	got, err := decodeIndexedList(trimmed)
	if err != nil {
		t.Fatal(err)
	}
	for i := range elements {
		if !bytes.Equal(got[i], elements[i]) {
			t.Fatalf("element %d differs", i)
		}
	}
	if _, ok := trimIndexedList(trimmed); ok {
		t.Fatal("trimming an exact list must be a no-op")
	}
}

func TestTrimIndexedListSmallCountKeepsPromoteCapacity(t *testing.T) {
	var elements [][]byte
	for i := 0; i < indexedListPromoteElements+3; i++ {
		elements = append(elements, []byte{byte(i)})
	}
	data, _ := encodeIndexedList(elements)
	// Simulate pops that left fewer elements than the promote threshold.
	data[3] = 5
	trimmed, ok := trimIndexedList(data)
	if !ok {
		t.Fatal("expected trim")
	}
	count, capacity, _, _, err := indexedListMeta(trimmed)
	if err != nil || count != 5 || capacity != indexedListPromoteElements {
		t.Fatalf("count=%d capacity=%d err=%v", count, capacity, err)
	}
}

// Compact must shrink idle lists, keep every element, and leave lists able to
// grow again with correct contents.
func TestCompactTrimsListHeadroomAndListsStillGrow(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	s := New()
	reference := map[string][][]byte{}
	for k := 0; k < 40; k++ {
		key := fmt.Sprintf("list:%d", k)
		n := 40 + rng.Intn(400)
		for i := 0; i < n; i++ {
			v := make([]byte, rng.Intn(90))
			rng.Read(v)
			if _, err := s.ListPushRight(key, [][]byte{v}); err != nil {
				t.Fatal(err)
			}
			reference[key] = append(reference[key], v)
		}
	}
	before := s.Memory().AccountedBytes
	if s.Compact(1<<30) == 0 {
		t.Fatal("expected compaction")
	}
	after := s.Memory().AccountedBytes
	if after >= before {
		t.Fatalf("memory did not shrink: before=%d after=%d", before, after)
	}
	check := func(stage string) {
		for key, want := range reference {
			got, err := s.ListRange(key, 0, -1)
			if err != nil || len(got) != len(want) {
				t.Fatalf("%s %s: len=%d want %d err=%v", stage, key, len(got), len(want), err)
			}
			for i := range want {
				if !bytes.Equal(got[i], want[i]) {
					t.Fatalf("%s %s: element %d differs", stage, key, i)
				}
			}
			idx := rng.Intn(len(want))
			v, ok, err := s.ListIndex(key, int64(idx))
			if err != nil || !ok || !bytes.Equal(v, want[idx]) {
				t.Fatalf("%s %s: LINDEX %d", stage, key, idx)
			}
		}
	}
	check("after compact")
	for key := range reference {
		for i := 0; i < 50; i++ {
			v := make([]byte, rng.Intn(90))
			rng.Read(v)
			if _, err := s.ListPushRight(key, [][]byte{v}); err != nil {
				t.Fatal(err)
			}
			reference[key] = append(reference[key], v)
		}
	}
	check("after regrow")
	t.Logf("memory before=%d after=%d", before, after)
}
