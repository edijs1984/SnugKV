package engine

import (
	"fmt"
	"math/rand"
	"testing"
)

// Growth rebuilds must keep every member, apply later duplicates in a batch,
// and report the right added count, for single and multi-member batches.
func TestIndexedZSetAddSimpleRebuildMatchesModel(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	seed := make([]ZSetItem, 0, indexedZSetPromoteMembers)
	model := map[string]float64{}
	for i := 0; i < indexedZSetPromoteMembers; i++ {
		m := fmt.Sprintf("m%d", i)
		seed = append(seed, ZSetItem{Member: []byte(m), Score: float64(i)})
		model[m] = float64(i)
	}
	data, err := encodeIndexedZSet(seed)
	if err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 400; round++ {
		n := 1 + rng.Intn(9)
		pairs := make([]ZSetItem, n)
		wantAdded := int64(0)
		next := map[string]float64{}
		for k, v := range model {
			next[k] = v
		}
		for i := range pairs {
			m := fmt.Sprintf("m%d", rng.Intn(300))
			sc := float64(rng.Intn(50)) + 0.5*float64(rng.Intn(2))
			pairs[i] = ZSetItem{Member: []byte(m), Score: sc}
			if _, ok := next[m]; !ok {
				wantAdded++
			}
			next[m] = sc
		}
		added, rebuilt, err := indexedZSetAddSimple(data, pairs)
		if err != nil {
			t.Fatal(err)
		}
		if added != wantAdded {
			t.Fatalf("round %d added=%d want %d", round, added, wantAdded)
		}
		if rebuilt != nil {
			data = rebuilt
		}
		model = next
		items, err := decodeIndexedZSet(data)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != len(model) {
			t.Fatalf("round %d len=%d want %d", round, len(items), len(model))
		}
		for _, it := range items {
			if model[string(it.Member)] != it.Score {
				t.Fatalf("round %d %s=%v want %v", round, it.Member, it.Score, model[string(it.Member)])
			}
		}
		if round%50 == 49 {
			if trimmed, ok := trimIndexedZSet(data); ok {
				data = trimmed
			}
		}
	}
}
