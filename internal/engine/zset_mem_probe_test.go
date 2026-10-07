package engine

import (
	"fmt"
	"testing"
)

// Reports bytes per item for the benchmark zset shapes (25 B members, integer scores).
func TestZSetSettledBytesPerItemReport(t *testing.T) {
	if testing.Short() {
		t.Skip("memory report")
	}
	for _, card := range []int{10, 50, 100, 200, 1000} {
		s := New()
		const items = 200_000
		for i := 0; i < items; i++ {
			m := fmt.Sprintf("m:%06d:%016x", i%card, uint64(i)*0x9E3779B97F4A7C15)
			if _, _, _, err := s.ZSetAdd(fmt.Sprintf("z:%d", i/card), []ZSetItem{{Member: []byte(m), Score: float64(i)}}, ZSetAddOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		raw := s.Memory().AccountedBytes
		s.Compact(1 << 30)
		settled := s.Memory().AccountedBytes
		t.Logf("card=%4d  right-after-load=%6.1f B/item  settled=%6.1f B/item", card, float64(raw)/items, float64(settled)/items)
	}
}
