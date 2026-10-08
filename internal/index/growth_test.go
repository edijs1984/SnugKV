package index

import (
	"strconv"
	"testing"
)

func fillTable(n int) *Table[uint32] {
	t := New[uint32]()
	for i := 0; i < n; i++ {
		t.Set("key:"+strconv.Itoa(i), uint32(i))
	}
	return t
}

// Growth must keep every key reachable and stay within the occupancy rule.
func TestGradualGrowthKeepsKeysAndBoundsSlack(t *testing.T) {
	for _, n := range []int{1, 4, 5, 100, 255, 256, 300, 1000, 3906, 5000, 20000} {
		tbl := fillTable(n)
		for i := 0; i < n; i++ {
			if v, ok := tbl.Get("key:" + strconv.Itoa(i)); !ok || v != uint32(i) {
				t.Fatalf("n=%d key %d lost", n, i)
			}
		}
		if tbl.Len() != n {
			t.Fatalf("n=%d len=%d", n, tbl.Len())
		}
		if !capacityAccepts(n, len(tbl.slots)) {
			t.Fatalf("n=%d capacity %d over the load ceiling", n, len(tbl.slots))
		}
		if n >= 1000 && len(tbl.slots) > n*2 {
			t.Fatalf("n=%d capacity %d wastes more than 2x", n, len(tbl.slots))
		}
	}
}

func TestGrowthBytesMatchesActualGrowth(t *testing.T) {
	tbl := New[uint32]()
	for i := 0; i < 5000; i++ {
		before := tbl.CapacityBytes()
		predicted := tbl.GrowthBytes(1)
		tbl.Set("key:"+strconv.Itoa(i), uint32(i))
		if got := tbl.CapacityBytes() - before; got != predicted {
			t.Fatalf("insert %d: predicted growth %d, actual %d", i, predicted, got)
		}
	}
}

func BenchmarkFillShard(b *testing.B) {
	keys := make([]string, 3906)
	for i := range keys {
		keys[i] = "key:" + strconv.Itoa(i)
	}
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		t := New[uint32]()
		for i, k := range keys {
			t.Set(k, uint32(i))
		}
	}
}
