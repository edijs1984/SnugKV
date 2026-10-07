package engine

import (
	"bytes"
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

// Randomized plain ZADD against a map reference. Batches include duplicate
// members, score changes, shared member prefixes (front coding), integer and
// fractional scores, and sizes that cross the packed -> indexed promotion point.
func TestZSetPlainAddMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	s := New()
	const key = "z"
	ref := map[string]float64{}

	randomScore := func() float64 {
		switch rng.Intn(4) {
		case 0:
			return float64(rng.Intn(50))
		case 1:
			return float64(rng.Intn(2000) - 1000)
		case 2:
			return rng.Float64()*100 - 50
		default:
			return float64(rng.Intn(5)) // many ties, ordering falls back to member bytes
		}
	}
	randomMember := func() []byte {
		return []byte(fmt.Sprintf("m:%03d:%s", rng.Intn(90), bytes.Repeat([]byte{'x'}, rng.Intn(6))))
	}

	check := func(step int) {
		items, err := s.ZSetRange(key, 0, -1, false)
		if err != nil {
			t.Fatalf("step %d: range: %v", step, err)
		}
		if len(items) != len(ref) {
			t.Fatalf("step %d: %d members, want %d", step, len(items), len(ref))
		}
		want := make([]ZSetItem, 0, len(ref))
		for m, sc := range ref {
			want = append(want, ZSetItem{Member: []byte(m), Score: sc})
		}
		sort.Slice(want, func(i, j int) bool { return zsetLess(want[i], want[j]) })
		for i := range want {
			if !bytes.Equal(items[i].Member, want[i].Member) || items[i].Score != want[i].Score {
				t.Fatalf("step %d: position %d got %q/%v want %q/%v", step, i, items[i].Member, items[i].Score, want[i].Member, want[i].Score)
			}
		}
		for m, sc := range ref {
			got, ok, err := s.ZSetScore(key, []byte(m))
			if err != nil || !ok || got != sc {
				t.Fatalf("step %d: ZSCORE %q = %v ok=%v err=%v want %v", step, m, got, ok, err, sc)
			}
		}
	}

	for step := 0; step < 600; step++ {
		n := 1 + rng.Intn(zsetPlainAddMaxPairs+2) // also exercises the generic path
		pairs := make([]ZSetItem, n)
		wantAdded := int64(0)
		seen := map[string]bool{}
		for i := range pairs {
			pairs[i] = ZSetItem{Member: randomMember(), Score: randomScore()}
			m := string(pairs[i].Member)
			if _, ok := ref[m]; !ok && !seen[m] {
				wantAdded++
			}
			seen[m] = true
			ref[m] = pairs[i].Score
		}
		added, _, _, err := s.ZSetAdd(key, pairs, ZSetAddOptions{})
		if err != nil {
			t.Fatalf("step %d: ZADD: %v", step, err)
		}
		if added != wantAdded {
			t.Fatalf("step %d: added=%d want %d", step, added, wantAdded)
		}
		if step%7 == 0 || len(ref) < 40 {
			check(step)
		}
	}
	check(-1)
}

func TestZSetPlainAddUnchangedScoreIsNoop(t *testing.T) {
	s := New()
	if _, _, _, err := s.ZSetAdd("z", []ZSetItem{{Member: []byte("a"), Score: 1}, {Member: []byte("b"), Score: 2}}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	added, _, _, err := s.ZSetAdd("z", []ZSetItem{{Member: []byte("a"), Score: 1}}, ZSetAddOptions{})
	if err != nil || added != 0 {
		t.Fatalf("added=%d err=%v", added, err)
	}
	card, _ := s.ZSetCard("z")
	if card != 2 {
		t.Fatalf("card=%d", card)
	}
}
