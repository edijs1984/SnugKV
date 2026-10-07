package index

import (
	"fmt"
	"math/rand"
	"testing"
)

func TestTightCapacityHoldsKeysAtNoMoreThanEightyPercent(t *testing.T) {
	for n := 1; n <= 20000; n++ {
		c := tightCapacity(n)
		if !capacityAccepts(n, c) {
			t.Fatalf("tightCapacity(%d)=%d does not accept %d keys", n, c, n)
		}
		if n > initialCapacity && c > n*5/4+16 {
			t.Fatalf("tightCapacity(%d)=%d is looser than needed", n, c)
		}
	}
}

func TestReserveShrinksPowerOfTwoTableToTightFit(t *testing.T) {
	table := New[uint32]()
	const n = 3906
	for i := 0; i < n; i++ {
		table.Set(fmt.Sprintf("counter:%d", i), uint32(i))
	}
	before := len(table.slots)
	table.Reserve(0)
	after := len(table.slots)
	if after >= before {
		t.Fatalf("Reserve did not shrink: before=%d after=%d", before, after)
	}
	if after != tightCapacity(n) {
		t.Fatalf("after=%d want %d", after, tightCapacity(n))
	}
	for i := 0; i < n; i++ {
		got, ok := table.Get(fmt.Sprintf("counter:%d", i))
		if !ok || got != uint32(i) {
			t.Fatalf("key %d lost after Reserve: got=%d ok=%t", i, got, ok)
		}
	}
	if _, ok := table.Get("missing"); ok {
		t.Fatal("missing key found")
	}
}

func TestTightTableGrowsAndMatchesModel(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for round := 0; round < 40; round++ {
		table := New[uint32]()
		model := map[string]uint32{}
		reserve := rng.Intn(300)
		table.Reserve(reserve)
		for op := 0; op < 2500; op++ {
			key := fmt.Sprintf("k%d", rng.Intn(700))
			switch rng.Intn(6) {
			case 0, 1, 2:
				v := uint32(rng.Intn(1 << 20))
				table.Set(key, v)
				model[key] = v
			case 3:
				table.Delete(key)
				delete(model, key)
			case 4:
				got, ok := table.Get(key)
				want, wok := model[key]
				if ok != wok || got != want {
					t.Fatalf("round %d op %d Get(%q)=(%d,%t) want (%d,%t)", round, op, key, got, ok, want, wok)
				}
			case 5:
				if rng.Intn(20) == 0 {
					table.Reserve(rng.Intn(900))
				}
			}
			if table.Len() != len(model) {
				t.Fatalf("round %d op %d len=%d want %d", round, op, table.Len(), len(model))
			}
		}
		seen := 0
		table.All()(func(k string, v uint32) bool {
			seen++
			if model[k] != v {
				t.Fatalf("All yielded %q=%d want %d", k, v, model[k])
			}
			return true
		})
		if seen != len(model) {
			t.Fatalf("All yielded %d keys want %d", seen, len(model))
		}
	}
}

func TestProbeWrapsAroundOddCapacity(t *testing.T) {
	table := New[uint32]()
	table.slots = make([]slot[uint32], 13)
	// All keys share one hash whose probe start is the last slot, so inserts
	// must wrap to slot 0 and keep going.
	hash := uint64(0xFFFFFFFF)
	if got := table.probeStart(hash); got != 12 {
		t.Fatalf("probeStart=%d want 12", got)
	}
	keys := []string{"a", "b", "c", "d", "e"}
	for i, k := range keys {
		testInsertHashed(table, k, uint32(i+1), hash)
	}
	for i, k := range keys {
		got, ok := table.GetHashed(k, hash)
		if !ok || got != uint32(i+1) {
			t.Fatalf("wrap lookup %q got=%d ok=%t", k, got, ok)
		}
	}
	if _, ok := table.GetHashed("zzz", hash); ok {
		t.Fatal("missing key found")
	}
}
