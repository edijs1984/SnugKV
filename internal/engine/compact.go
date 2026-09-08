package engine

import (
	"snugkv/internal/arena"
	"sort"
	"sync/atomic"
)

// Compact reclaims unused arena segments and index slots one shard at a time.
// It skips shards whose conservative transient-copy estimate exceeds scratch.
func (s *Store) Compact(scratch uint64) int {
	compacted := 0
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.Lock()
		oldArena, oldIndex := sh.arena.MemoryBytes(), sh.data.CapacityBytes()
		if oldArena+oldIndex == 0 || (oldArena+oldIndex)*3 > scratch {
			sh.mu.Unlock()
			continue
		}
		type pair struct {
			key   string
			value entry
		}
		items := make([]pair, 0, sh.data.Len())
		for key, e := range sh.data.All() {
			items = append(items, pair{key, e})
		}
		sort.Slice(items, func(i, j int) bool { return len(items[i].value.value) > len(items[j].value.value) })
		lengths := make([]int, len(items))
		for j := range items {
			lengths[j] = len(items[j].value.value)
		}
		var fresh arena.Arena
		projected := fresh.GrowthFor(lengths)
		s.memory.mu.Lock()
		if s.memory.max > 0 && s.memory.used-oldArena+projected > s.memory.max {
			s.memory.mu.Unlock()
			sh.mu.Unlock()
			continue
		}
		for _, item := range items {
			e := item.value
			e.ref = fresh.Alloc(e.value)
			e.value, _ = fresh.View(e.ref)
			e.version = atomic.AddUint64(&s.version, 1)
			sh.data.Set(item.key, e)
		}
		sh.arena = fresh
		sh.data.Compact()
		newArena, newIndex := sh.arena.MemoryBytes(), sh.data.CapacityBytes()
		s.memory.used = s.memory.used - oldArena - oldIndex + newArena + newIndex
		s.memory.arenas = s.memory.arenas - oldArena + newArena
		s.memory.index = s.memory.index - oldIndex + newIndex
		s.memory.mu.Unlock()
		sh.mu.Unlock()
		compacted++
	}
	return compacted
}
