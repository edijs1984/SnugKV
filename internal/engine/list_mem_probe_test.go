package engine

import (
	"bytes"
	"fmt"
	"testing"
)

// Reports settled bytes per item for the benchmark list shapes (64 B values).
func TestListSettledBytesPerItemReport(t *testing.T) {
	if testing.Short() {
		t.Skip("memory report")
	}
	for _, card := range []int{10, 100, 1000} {
		s := New()
		const items = 200_000
		v := bytes.Repeat([]byte("v"), 64)
		for i := 0; i < items; i++ {
			if _, err := s.ListPushRight(fmt.Sprintf("list:%d", i/card), [][]byte{v}); err != nil {
				t.Fatal(err)
			}
		}
		raw := s.Memory().AccountedBytes
		s.Compact(1 << 30)
		settled := s.Memory().AccountedBytes
		t.Logf("card=%4d  right-after-load=%6.1f B/item  settled=%6.1f B/item", card, float64(raw)/items, float64(settled)/items)
	}
}
