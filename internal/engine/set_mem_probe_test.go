package engine

import (
	"fmt"
	"testing"
)

// Reports bytes per member for the benchmark set shapes (25 B members).
func TestSetSettledBytesPerItemReport(t *testing.T) {
	if testing.Short() {
		t.Skip("memory report")
	}
	for _, card := range []int{10, 100, 1000} {
		s := New()
		const items = 200_000
		for i := 0; i < items; i++ {
			m := []byte(fmt.Sprintf("m:%06d:%016x", i%card, uint64(i+1)*0x9e3779b97f4a7c15))
			if _, err := s.SetAdd(fmt.Sprintf("set:%d", i/card), [][]byte{m}); err != nil {
				t.Fatal(err)
			}
		}
		m := s.Memory()
		raw := m.AccountedBytes
		t.Logf("card=%4d arena=%.1f payload=%.1f liveBlocks=%.1f index=%.1f entries=%.1f B/item", card,
			float64(m.ArenaBytes)/items, float64(m.ArenaPayloadBytes)/items, float64(m.ArenaLiveBlockBytes)/items,
			float64(m.IndexReservedBytes)/items, float64(m.EntryBytes)/items)
		s.Compact(1 << 30)
		t.Logf("card=%4d right-after-load=%6.1f settled=%6.1f B/item", card, float64(raw)/items, float64(s.Memory().AccountedBytes)/items)
	}
}
