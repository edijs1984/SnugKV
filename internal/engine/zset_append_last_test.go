package engine

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

// Single-member adds, mostly ascending but with random scores, duplicates,
// score updates, long members and non-integer scores mixed in, against a model.
func TestZSetAppendLastMatchesModel(t *testing.T) {
	for seed := int64(1); seed <= 6; seed++ {
		rng := rand.New(rand.NewSource(seed))
		s := New()
		model := map[string]float64{}
		next := 0.0
		for step := 0; step < 3000; step++ {
			var member string
			switch rng.Intn(6) {
			case 0:
				member = fmt.Sprintf("m:%06d:%016x", rng.Intn(40), rng.Uint64())
			case 1:
				b := make([]byte, 250+rng.Intn(20))
				for i := range b {
					b[i] = byte('a' + rng.Intn(3))
				}
				member = string(b)
			default:
				member = fmt.Sprintf("member-%d", rng.Intn(60))
			}
			var score float64
			switch rng.Intn(8) {
			case 0:
				score = rng.Float64() * 100
			case 1:
				score = float64(rng.Intn(50))
			case 2:
				score = next - float64(rng.Intn(5))
			default:
				next += float64(rng.Intn(3))
				score = next
			}
			if _, _, _, err := s.ZSetAdd("z", []ZSetItem{{Member: []byte(member), Score: score}}, ZSetAddOptions{}); err != nil {
				t.Fatal(err)
			}
			model[member] = score
			if rng.Intn(25) == 0 {
				victim := fmt.Sprintf("member-%d", rng.Intn(60))
				if _, err := s.ZSetRemove("z", [][]byte{[]byte(victim)}); err != nil {
					t.Fatal(err)
				}
				delete(model, victim)
			}
			if step%100 == 99 {
				items, err := s.ZSetRange("z", 0, -1, false)
				if err != nil {
					t.Fatal(err)
				}
				if len(items) != len(model) {
					t.Fatalf("seed %d step %d: len %d want %d", seed, step, len(items), len(model))
				}
				want := make([]ZSetItem, 0, len(model))
				for m, sc := range model {
					want = append(want, ZSetItem{Member: []byte(m), Score: sc})
				}
				sort.Slice(want, func(i, j int) bool { return zsetLess(want[i], want[j]) })
				for i := range want {
					if string(items[i].Member) != string(want[i].Member) || items[i].Score != want[i].Score {
						t.Fatalf("seed %d step %d: item %d got %q/%v want %q/%v", seed, step, i, items[i].Member, items[i].Score, want[i].Member, want[i].Score)
					}
				}
				for m, sc := range model {
					got, ok, err := s.ZSetScore("z", []byte(m))
					if err != nil || !ok || got != sc {
						t.Fatalf("seed %d step %d: score %q got %v/%v want %v err %v", seed, step, m, got, ok, sc, err)
					}
				}
			}
		}
	}
}

func TestPackedZSetAppendLastFallsBack(t *testing.T) {
	items := []ZSetItem{{Member: []byte("a"), Score: 1}, {Member: []byte("b"), Score: 2}}
	data, err := encodePackedZSet(items)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := packedZSetAppendLast(data, []byte("a"), 3, nil); ok {
		t.Fatal("existing member must fall back")
	}
	if _, ok := packedZSetAppendLast(data, []byte("c"), 1, nil); ok {
		t.Fatal("non-last member must fall back")
	}
	out, ok := packedZSetAppendLast(data, []byte("c"), 3, nil)
	if !ok {
		t.Fatal("expected fast path")
	}
	got, err := decodePackedZSet(out)
	if err != nil || len(got) != 3 || string(got[2].Member) != "c" || got[2].Score != 3 {
		t.Fatalf("bad result %v err %v", got, err)
	}
}

// Ascending adds must not leave the value in a larger encoding than a full
// re-encode would choose (front-coding of members sharing a prefix).
func TestZSetAppendLastKeepsBestEncoding(t *testing.T) {
	s := New()
	var items []ZSetItem
	for i := 0; i < 20; i++ {
		m := []byte(fmt.Sprintf("m:%06d:%016x", i, uint64(i)*0x9E3779B97F4A7C15))
		items = append(items, ZSetItem{Member: m, Score: float64(1000 + i)})
		if _, _, _, err := s.ZSetAdd("z", []ZSetItem{items[i]}, ZSetAddOptions{}); err != nil {
			t.Fatal(err)
		}
		st, ok, err := s.ZSetStorageStats("z")
		if err != nil || !ok {
			t.Fatal(err)
		}
		want, err := encodePackedZSet(items)
		if err != nil {
			t.Fatal(err)
		}
		if st.StoredBytes > len(want)+1 {
			t.Fatalf("after %d members stored %d bytes, full encode is %d", i+1, st.StoredBytes, len(want))
		}
	}
}
