package optimizer

import (
	"bytes"
	"fmt"
	"testing"

	"snugkv/internal/engine"
)

// Lists grown by repeated pushes leave freed blocks in shared segments. Idle
// compaction must reclaim them, and once it has, the same policy must not ask
// for another pass (the tail segment each shard keeps is not reclaimable).
func TestGrownListsTriggerCompactionOnceThenSettle(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 100k-item store")
	}
	store := engine.New()
	v := bytes.Repeat([]byte("v"), 64)
	const items, card = 200_000, 1000
	for i := 0; i < items; i++ {
		if _, err := store.ListPushRight(fmt.Sprintf("list:%d", i/card), [][]byte{v}); err != nil {
			t.Fatal(err)
		}
	}
	m := store.Memory()
	if !shouldCompactArena(m.ArenaBytes, m.ArenaLiveBlockBytes, 0) {
		t.Fatalf("grown lists not compacted: arena=%d live=%d", m.ArenaBytes, m.ArenaLiveBlockBytes)
	}
	before := m.AccountedBytes
	store.Compact(1 << 30)
	m = store.Memory()
	if shouldCompactArena(m.ArenaBytes, m.ArenaLiveBlockBytes, 0) {
		t.Fatalf("compaction would repeat: arena=%d live=%d", m.ArenaBytes, m.ArenaLiveBlockBytes)
	}
	t.Logf("accounted %.1f -> %.1f B/item", float64(before)/items, float64(m.AccountedBytes)/items)
}
