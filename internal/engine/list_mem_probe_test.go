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
	for _, card := range []int{10, 100, 200, 500, 1000, 3000} {
		s := New()
		const items = 200_000
		v := bytes.Repeat([]byte("v"), 64)
		for i := 0; i < items; i++ {
			if _, err := s.ListPushRight(fmt.Sprintf("list:%d", i/card), [][]byte{v}); err != nil {
				t.Fatal(err)
			}
		}
		m := s.Memory()
		raw := m.AccountedBytes
		t.Logf("card=%4d  arena=%.1f payload=%.1f liveBlocks=%.1f index=%.1f entries=%.1f B/item", card,
			float64(m.ArenaBytes)/items, float64(m.ArenaPayloadBytes)/items, float64(m.ArenaLiveBlockBytes)/items,
			float64(m.IndexReservedBytes)/items, float64(m.EntryBytes)/items)
		s.Compact(1 << 30)
		settled := s.Memory().AccountedBytes
		t.Logf("card=%4d  right-after-load=%6.1f B/item  settled=%6.1f B/item", card, float64(raw)/items, float64(settled)/items)
	}
}
