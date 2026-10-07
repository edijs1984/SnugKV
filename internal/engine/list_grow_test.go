package engine

import (
	"bytes"
	"math/rand"
	"testing"
)

// Randomized RPUSH against a plain slice reference. Sizes cross the packed ->
// indexed promotion point and force repeated headroom/capacity growth, including
// empty values and multi-byte length varints.
func TestListPushRightGrowthMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	s := New()
	const key = "grow"
	var reference [][]byte

	randomValue := func() []byte {
		var n int
		switch rng.Intn(10) {
		case 0:
			n = 0
		case 1:
			n = 130 + rng.Intn(400) // multi-byte uvarint length
		default:
			n = rng.Intn(80)
		}
		v := make([]byte, n)
		rng.Read(v)
		return v
	}

	for step := 0; step < 1500; step++ {
		batch := 1
		if rng.Intn(6) == 0 {
			batch = 2 + rng.Intn(5)
		}
		values := make([][]byte, batch)
		for i := range values {
			values[i] = randomValue()
			reference = append(reference, append([]byte(nil), values[i]...))
		}
		got, err := s.ListPushRight(key, values)
		if err != nil {
			t.Fatalf("step %d: RPUSH: %v", step, err)
		}
		if int(got) != len(reference) {
			t.Fatalf("step %d: RPUSH length=%d want %d", step, got, len(reference))
		}

		// Spot check a few positions every step, full comparison periodically.
		for probe := 0; probe < 3; probe++ {
			idx := rng.Intn(len(reference))
			value, ok, err := s.ListIndex(key, int64(idx))
			if err != nil || !ok || !bytes.Equal(value, reference[idx]) {
				t.Fatalf("step %d: LINDEX %d ok=%v err=%v got=%x want=%x", step, idx, ok, err, value, reference[idx])
			}
		}
		if step%97 == 0 || step == 1499 {
			all, err := s.ListRange(key, 0, -1)
			if err != nil {
				t.Fatalf("step %d: LRANGE: %v", step, err)
			}
			if len(all) != len(reference) {
				t.Fatalf("step %d: LRANGE len=%d want %d", step, len(all), len(reference))
			}
			for i := range reference {
				if !bytes.Equal(all[i], reference[i]) {
					t.Fatalf("step %d: element %d differs", step, i)
				}
			}
		}
	}

	// The list must still behave after growth: pops see the right ends.
	head, err := s.ListPopLeft(key, 1)
	if err != nil || len(head) != 1 || !bytes.Equal(head[0], reference[0]) {
		t.Fatalf("LPOP got=%d err=%v", len(head), err)
	}
	tail, err := s.ListPopRight(key, 1)
	if err != nil || len(tail) != 1 || !bytes.Equal(tail[0], reference[len(reference)-1]) {
		t.Fatalf("RPOP got=%d err=%v", len(tail), err)
	}
}

func TestGrowIndexedListRawPreservesElementsAndOrder(t *testing.T) {
	var elements [][]byte
	for i := 0; i < indexedListPromoteElements+5; i++ {
		elements = append(elements, bytes.Repeat([]byte{byte(i)}, i%7))
	}
	data, err := encodeIndexedList(elements)
	if err != nil {
		t.Fatal(err)
	}
	extra := [][]byte{[]byte("x"), {}, bytes.Repeat([]byte("y"), 300)}
	count, grown, ok, err := growIndexedListRaw(data, extra)
	if err != nil || !ok {
		t.Fatalf("grow ok=%v err=%v", ok, err)
	}
	want := append(append([][]byte(nil), elements...), extra...)
	if count != len(want) {
		t.Fatalf("count=%d want %d", count, len(want))
	}
	got, err := decodeIndexedList(grown)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("decoded %d elements want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("element %d differs", i)
		}
	}
}

// Unreferenced payload (e.g. left by earlier removals) must not be copied
// forward; the caller falls back to the compacting rebuild.
func TestGrowIndexedListRawDeclinesWhenPayloadHasDeadSpace(t *testing.T) {
	var elements [][]byte
	for i := 0; i < indexedListPromoteElements+2; i++ {
		elements = append(elements, []byte{byte('a' + i%26)})
	}
	data, err := encodeIndexedList(elements)
	if err != nil {
		t.Fatal(err)
	}
	// Claim one extra payload byte that no element references.
	_, _, used, _, err := indexedListMeta(data)
	if err != nil {
		t.Fatal(err)
	}
	data[11] = byte(used + 1)
	data[12] = byte((used + 1) >> 8)
	data[13] = byte((used + 1) >> 16)
	data[14] = byte((used + 1) >> 24)
	if _, _, ok, err := growIndexedListRaw(data, [][]byte{[]byte("z")}); err != nil || ok {
		t.Fatalf("expected decline, ok=%v err=%v", ok, err)
	}
	// The public append path must still succeed via the fallback.
	count, rebuilt, err := indexedListAppend(data, [][]byte{[]byte("z"), bytes.Repeat([]byte("q"), 2000)})
	if err != nil || rebuilt == nil {
		t.Fatalf("fallback append rebuilt=%v err=%v", rebuilt != nil, err)
	}
	if count != len(elements)+2 {
		t.Fatalf("count=%d want %d", count, len(elements)+2)
	}
}
