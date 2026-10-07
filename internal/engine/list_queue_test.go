package engine

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"
)

// Mixes pushes, pops, ranges and indexes on a list that crosses the indexed
// threshold both ways, against a slice model, with compactions in between.
func TestListQueueOpsMatchModel(t *testing.T) {
	for seed := int64(1); seed <= 4; seed++ {
		rng := rand.New(rand.NewSource(seed))
		s := New()
		var model [][]byte
		val := func() []byte {
			return bytes.Repeat([]byte{byte('a' + rng.Intn(26))}, rng.Intn(150))
		}
		check := func(step int) {
			t.Helper()
			got, err := s.ListRange("q", 0, -1)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(model) {
				t.Fatalf("seed %d step %d: len %d want %d", seed, step, len(got), len(model))
			}
			for i := range got {
				if !bytes.Equal(got[i], model[i]) {
					t.Fatalf("seed %d step %d: element %d differs", seed, step, i)
				}
			}
			n := int64(len(model))
			for k := 0; k < 8 && n > 0; k++ {
				a, b := rng.Int63n(n+4)-2, rng.Int63n(n+4)-2
				if rng.Intn(2) == 0 {
					a -= n
				}
				r, err := s.ListRange("q", a, b)
				if err != nil {
					t.Fatal(err)
				}
				want := refRange(model, a, b)
				if len(r) != len(want) {
					t.Fatalf("seed %d step %d: range %d..%d len %d want %d", seed, step, a, b, len(r), len(want))
				}
				for i := range r {
					if !bytes.Equal(r[i], want[i]) {
						t.Fatalf("seed %d step %d: range %d..%d element %d", seed, step, a, b, i)
					}
				}
				i := rng.Int63n(n)
				v, ok, err := s.ListIndex("q", i)
				if err != nil || !ok || !bytes.Equal(v, model[i]) {
					t.Fatalf("seed %d step %d: index %d", seed, step, i)
				}
			}
		}
		for step := 0; step < 4000; step++ {
			// Phase 1 grows, phase 2 drains, phase 3 hovers (queue behaviour).
			push, pop := 5, 5
			switch {
			case step < 1200:
				push, pop = 7, 3
			case step < 2400:
				push, pop = 3, 7
			}
			switch r := rng.Intn(10); {
			case r < push:
				v := val()
				if rng.Intn(8) == 0 {
					if _, err := s.ListPushLeft("q", [][]byte{v}); err != nil {
						t.Fatal(err)
					}
					model = append([][]byte{v}, model...)
				} else {
					if _, err := s.ListPushRight("q", [][]byte{v}); err != nil {
						t.Fatal(err)
					}
					model = append(model, v)
				}
			case r < push+pop:
				cnt := 1
				if rng.Intn(5) == 0 {
					cnt = 1 + rng.Intn(6)
				}
				left := rng.Intn(2) == 0
				var got [][]byte
				var err error
				if left {
					got, err = s.ListPopLeft("q", cnt)
				} else {
					got, err = s.ListPopRight("q", cnt)
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(model) == 0 {
					if len(got) != 0 {
						t.Fatalf("pop on empty returned %d", len(got))
					}
					continue
				}
				n := cnt
				if n > len(model) {
					n = len(model)
				}
				if len(got) != n {
					t.Fatalf("seed %d step %d: popped %d want %d", seed, step, len(got), n)
				}
				for i := 0; i < n; i++ {
					var want []byte
					if left {
						want = model[i]
					} else {
						want = model[len(model)-1-i]
					}
					if !bytes.Equal(got[i], want) {
						t.Fatalf("seed %d step %d: pop element %d", seed, step, i)
					}
				}
				if left {
					model = model[n:]
				} else {
					model = model[:len(model)-n]
				}
			}
			if step%250 == 249 {
				check(step)
			}
			if step%700 == 699 {
				s.Compact(1 << 30)
				check(step)
			}
		}
		check(-1)
		_ = fmt.Sprint
	}
}

func refRange(model [][]byte, start, stop int64) [][]byte {
	n := int64(len(model))
	if start < 0 {
		start += n
	}
	if stop < 0 {
		stop += n
	}
	if start < 0 {
		start = 0
	}
	if stop < 0 || start >= n || start > stop {
		return nil
	}
	if stop >= n {
		stop = n - 1
	}
	return model[start : stop+1]
}

func TestListQueueKeepsIndexedAndShrinks(t *testing.T) {
	s := New()
	v := bytes.Repeat([]byte("v"), 64)
	for i := 0; i < 5000; i++ {
		if _, err := s.ListPushRight("q", [][]byte{v}); err != nil {
			t.Fatal(err)
		}
	}
	// Steady-state queue: pop one, push one, many times.
	for i := 0; i < 20000; i++ {
		if _, err := s.ListPopLeft("q", 1); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ListPushRight("q", [][]byte{v}); err != nil {
			t.Fatal(err)
		}
	}
	st, ok, err := s.ListStorageStats("q")
	if err != nil || !ok || st.Encoding != "indexed" {
		t.Fatalf("expected indexed list, got %+v ok=%v err=%v", st, ok, err)
	}
	before := s.Memory().AccountedBytes
	s.Compact(1 << 30)
	if s.Memory().AccountedBytes > before {
		t.Fatalf("compaction grew memory")
	}
	if n, _ := s.ListLen("q"); n != 5000 {
		t.Fatalf("len %d", n)
	}
}
