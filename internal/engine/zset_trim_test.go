package engine

import (
	"fmt"
	"testing"
)

func TestCompactTrimsIndexedZSet(t *testing.T) {
	s := New()
	want := map[string]float64{}
	add := func(i int) {
		m := fmt.Sprintf("m:%06d:%016x", i, uint64(i)*0x9E3779B97F4A7C15)
		want[m] = float64(i * 3)
		if _, _, _, err := s.ZSetAdd("z", []ZSetItem{{Member: []byte(m), Score: float64(i * 3)}}, ZSetAddOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 100; i++ {
		add(i)
	}
	before := s.Memory().AccountedBytes
	s.Compact(1 << 30)
	after := s.Memory().AccountedBytes
	if after >= before {
		t.Fatalf("expected trim to shrink: before=%d after=%d", before, after)
	}
	check := func() {
		t.Helper()
		items, err := s.ZSetRange("z", 0, -1, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != len(want) {
			t.Fatalf("len %d want %d", len(items), len(want))
		}
		for _, it := range items {
			if sc, ok := want[string(it.Member)]; !ok || sc != it.Score {
				t.Fatalf("member %q score %v want %v", it.Member, it.Score, sc)
			}
		}
	}
	check()
	for i := 100; i < 140; i++ {
		add(i)
	}
	check()
	s.Compact(1 << 30)
	check()
}
