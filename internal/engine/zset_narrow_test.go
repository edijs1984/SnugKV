package engine

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"testing"
)

// Grows a zset across the 2-byte/4-byte slot boundary with mixed integer and
// fractional scores, increments, updates, removals and compactions, comparing
// against a map model.
func TestIndexedZSetNarrowWideRandomized(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	s := New()
	model := map[string]float64{}
	member := func(i int) string { return fmt.Sprintf("member-%05d-%s", i, "xxxxxxxxxxxxxxxxxxxxxxxxxxxx") }
	randScore := func() float64 {
		switch rng.Intn(4) {
		case 0:
			return float64(rng.Intn(2_000_000) - 1_000_000)
		case 1:
			return rng.Float64()*1000 - 500
		case 2:
			return float64(rng.Int63n(1<<53)) * float64(1-2*rng.Intn(2))
		default:
			return float64(rng.Intn(10))
		}
	}
	verify := func(step int) {
		t.Helper()
		items, err := s.ZSetRange("z", 0, -1, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != len(model) {
			t.Fatalf("step %d: len %d want %d", step, len(items), len(model))
		}
		if !sort.SliceIsSorted(items, func(i, j int) bool { return zsetLess(items[i], items[j]) }) {
			t.Fatalf("step %d: not sorted", step)
		}
		for _, it := range items {
			if want, ok := model[string(it.Member)]; !ok || want != it.Score {
				t.Fatalf("step %d: %q got %v want %v", step, it.Member, it.Score, want)
			}
		}
		for k := 0; k < 20; k++ {
			m := member(rng.Intn(4000))
			got, ok, err := s.ZSetScore("z", []byte(m))
			want, wok := model[m]
			if err != nil || ok != wok || (ok && got != want) {
				t.Fatalf("step %d: score %q got %v/%v want %v/%v err %v", step, m, got, ok, want, wok, err)
			}
		}
	}
	for step := 0; step < 6000; step++ {
		m := member(rng.Intn(4000))
		switch op := rng.Intn(10); {
		case op < 6:
			sc := randScore()
			if _, _, _, err := s.ZSetAdd("z", []ZSetItem{{Member: []byte(m), Score: sc}}, ZSetAddOptions{}); err != nil {
				t.Fatal(err)
			}
			model[m] = sc
		case op < 8:
			inc := float64(rng.Intn(100) - 50)
			_, _, got, err := s.ZSetAdd("z", []ZSetItem{{Member: []byte(m), Score: inc}}, ZSetAddOptions{INCR: true})
			if err != nil {
				t.Fatal(err)
			}
			model[m] += inc
			if model[m] == 0 {
				model[m] = 0
			}
			if got != model[m] && !(math.IsNaN(got)) {
				t.Fatalf("incr got %v want %v", got, model[m])
			}
		default:
			if _, err := s.ZSetRemove("z", [][]byte{[]byte(m)}); err != nil {
				t.Fatal(err)
			}
			delete(model, m)
		}
		if step%500 == 499 {
			verify(step)
		}
		if step%1500 == 1499 {
			s.Compact(1 << 30)
			verify(step)
		}
	}
	verify(-1)
}
