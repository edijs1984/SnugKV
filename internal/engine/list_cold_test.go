package engine

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"
)

func randomColdElements(rng *rand.Rand, n int) [][]byte {
	elements := make([][]byte, n)
	for i := range elements {
		size := rng.Intn(40)
		if rng.Intn(20) == 0 {
			size = rng.Intn(400)
		}
		elements[i] = make([]byte, size)
		rng.Read(elements[i])
	}
	return elements
}

func TestColdListMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	for round := 0; round < 300; round++ {
		n := coldListMinElements + rng.Intn(1500)
		if round%25 == 0 {
			n = 9000 + rng.Intn(3000) // pushes the payload past 64 KiB
		}
		elements := randomColdElements(rng, n)
		cold, err := encodeColdList(elements)
		if err != nil {
			t.Fatal(err)
		}
		if !isColdList(cold) || isIndexedList(cold) {
			t.Fatal("cold list header misdetected")
		}
		decoded, err := decodeColdList(cold)
		if err != nil || len(decoded) != n {
			t.Fatalf("decode n=%d len=%d err=%v", n, len(decoded), err)
		}
		for i := range elements {
			if !bytes.Equal(decoded[i], elements[i]) {
				t.Fatalf("decode mismatch at %d", i)
			}
		}
		for probe := 0; probe < 200; probe++ {
			idx := rng.Intn(n+4) - 2
			got, found, err := coldListElement(cold, idx)
			want := idx
			if want < 0 {
				want += n
			}
			if err != nil {
				t.Fatal(err)
			}
			if want < 0 || want >= n {
				if found {
					t.Fatalf("index %d found out of range", idx)
				}
				continue
			}
			if !found || !bytes.Equal(got, elements[want]) {
				t.Fatalf("index %d mismatch", idx)
			}
		}
		for probe := 0; probe < 100; probe++ {
			start := int64(rng.Intn(n+6) - 3)
			stop := int64(rng.Intn(n+6) - 3)
			got, err := coldListRange(cold, start, stop)
			if err != nil {
				t.Fatal(err)
			}
			s, e := start, stop
			if s < 0 {
				s += int64(n)
			}
			if e < 0 {
				e += int64(n)
			}
			if s < 0 {
				s = 0
			}
			var want [][]byte
			if !(e < 0 || s >= int64(n) || s > e) {
				if e >= int64(n) {
					e = int64(n) - 1
				}
				want = elements[s : e+1]
			}
			if len(got) != len(want) {
				t.Fatalf("range %d..%d len=%d want=%d", start, stop, len(got), len(want))
			}
			for i := range want {
				if !bytes.Equal(got[i], want[i]) {
					t.Fatalf("range %d..%d mismatch at %d", start, stop, i)
				}
			}
		}
	}
}

func TestColdListRejectsCorruptData(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	cold, err := encodeColdList(randomColdElements(rng, 200))
	if err != nil {
		t.Fatal(err)
	}
	for cut := 0; cut < len(cold); cut += 7 {
		truncated := cold[:cut]
		if _, err := decodeColdList(truncated); err == nil && cut != len(cold) {
			t.Fatalf("truncated list (%d of %d bytes) decoded", cut, len(cold))
		}
		coldListElement(truncated, 100)
		coldListRange(truncated, 0, -1)
	}
}

// TestColdListSurvivesCompactionAndWrites keeps a model of one list and applies
// every list command to the store while compactions turn it into the cold
// layout in between.
func TestColdListSurvivesCompactionAndWrites(t *testing.T) {
	store := New()
	rng := rand.New(rand.NewSource(5))
	const key = "cold"
	var model [][]byte
	value := func(step int) []byte {
		return []byte(fmt.Sprintf("v%d-%d", step, rng.Intn(100000)))
	}
	check := func(step int, what string) {
		t.Helper()
		n, err := store.ListLen(key)
		if err != nil || int(n) != len(model) {
			t.Fatalf("step %d %s: len=%d err=%v want %d", step, what, n, err, len(model))
		}
		if len(model) == 0 {
			return
		}
		got, err := store.ListRange(key, 0, -1)
		if err != nil || len(got) != len(model) {
			t.Fatalf("step %d %s: range len=%d err=%v want %d", step, what, len(got), err, len(model))
		}
		for i := range model {
			if !bytes.Equal(got[i], model[i]) {
				t.Fatalf("step %d %s: element %d mismatch", step, what, i)
			}
		}
		for probe := 0; probe < 5; probe++ {
			i := rng.Intn(len(model))
			v, found, err := store.ListIndex(key, int64(i))
			if err != nil || !found || !bytes.Equal(v, model[i]) {
				t.Fatalf("step %d %s: index %d found=%v err=%v", step, what, i, found, err)
			}
			v, found, err = store.ListIndex(key, int64(i-len(model)))
			if err != nil || !found || !bytes.Equal(v, model[i]) {
				t.Fatalf("step %d %s: negative index %d", step, what, i-len(model))
			}
		}
	}
	sawCold := false
	for step := 0; step < 1500; step++ {
		switch op := rng.Intn(14); op {
		case 0, 1, 2:
			n := 1 + rng.Intn(30)
			vs := make([][]byte, n)
			for i := range vs {
				vs[i] = value(step)
			}
			if _, err := store.ListPushRight(key, vs); err != nil {
				t.Fatal(err)
			}
			model = append(model, vs...)
		case 3:
			vs := [][]byte{value(step), value(step)}
			if _, err := store.ListPushLeft(key, vs); err != nil {
				t.Fatal(err)
			}
			model = append([][]byte{vs[1], vs[0]}, model...)
		case 4:
			if len(model) > 0 {
				n := 1 + rng.Intn(3)
				if n > len(model) {
					n = len(model)
				}
				got, err := store.ListPopLeft(key, n)
				if err != nil || len(got) != n {
					t.Fatalf("pop left: %v %d", err, len(got))
				}
				model = model[n:]
			}
		case 5:
			if len(model) > 0 {
				n := 1 + rng.Intn(3)
				if n > len(model) {
					n = len(model)
				}
				if _, err := store.ListPopRight(key, n); err != nil {
					t.Fatal(err)
				}
				model = model[:len(model)-n]
			}
		case 6:
			if len(model) > 0 {
				i := rng.Intn(len(model))
				v := value(step)
				if err := store.ListSet(key, int64(i), v); err != nil {
					t.Fatal(err)
				}
				model[i] = v
			}
		case 7:
			if len(model) > 3 {
				i := rng.Intn(len(model))
				pivot := model[i]
				v := value(step)
				if _, err := store.ListInsert(key, true, pivot, v); err != nil {
					t.Fatal(err)
				}
				first := 0
				for first < len(model) && !bytes.Equal(model[first], pivot) {
					first++
				}
				model = append(model[:first], append([][]byte{v}, model[first:]...)...)
			}
		case 8:
			if len(model) > 3 {
				v := model[rng.Intn(len(model))]
				removed, err := store.ListRemove(key, 1, v)
				if err != nil || removed != 1 {
					t.Fatalf("remove: %v %d", err, removed)
				}
				for i := range model {
					if bytes.Equal(model[i], v) {
						model = append(model[:i], model[i+1:]...)
						break
					}
				}
			}
		case 9:
			if len(model) > 50 && rng.Intn(4) == 0 {
				if err := store.ListTrim(key, 1, int64(len(model)-2)); err != nil {
					t.Fatal(err)
				}
				model = model[1 : len(model)-1]
			}
		default:
			store.Compact(^uint64(0))
			if len(model) > 0 {
				if stats, ok, err := store.ListStorageStats(key); err == nil && ok && stats.Encoding == "cold" {
					sawCold = true
				}
			}
			check(step, "after compact")
			continue
		}
		if step%7 == 0 {
			check(step, "after write")
		}
	}
	if !sawCold {
		t.Fatal("compaction never produced a cold list; the test is not exercising the layout")
	}
}

func TestCompactStoresLargeIdleListCold(t *testing.T) {
	store := New()
	values := make([][]byte, 1000)
	for i := range values {
		values[i] = bytes.Repeat([]byte{byte('a' + i%26)}, 64)
	}
	for i := 0; i < len(values); i += 100 {
		if _, err := store.ListPushRight("big", values[i:i+100]); err != nil {
			t.Fatal(err)
		}
	}
	store.Compact(^uint64(0))
	stats, ok, err := store.ListStorageStats("big")
	if err != nil || !ok {
		t.Fatalf("stats ok=%v err=%v", ok, err)
	}
	if stats.Encoding != "cold" {
		t.Fatalf("1000 idle elements stored as %q, want cold", stats.Encoding)
	}
	if stats.StoredBytes > 65536-8 {
		t.Fatalf("cold list uses %d bytes; expected it to fit one 64 KiB block", stats.StoredBytes)
	}
	v, found, err := store.ListIndex("big", 777)
	if err != nil || !found || !bytes.Equal(v, values[777]) {
		t.Fatalf("index 777 found=%v err=%v", found, err)
	}
}
