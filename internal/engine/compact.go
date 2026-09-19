package engine

import (
	"snugkv/internal/arena"
	"sort"
)

// Compact reclaims unused arena segments and index slots one shard at a time.
// It skips shards whose conservative transient-copy estimate exceeds scratch.
func (s *Store) Compact(scratch uint64) int {
	compacted := 0
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.Lock()
		oldArena, oldIndex := sh.arena.TotalMemoryBytes(), sh.data.CapacityBytes()
		if oldArena+oldIndex == 0 || (oldArena+oldIndex)*3 > scratch {
			sh.mu.Unlock()
			continue
		}
		type pair struct {
			key   string
			value entry
		}
		items := make([]pair, 0, sh.data.Len())
		for key, e := range sh.all() {
			items = append(items, pair{key, e})
		}
		sort.Slice(items, func(i, j int) bool {
			return len(sh.encoded(items[i].value)) >
				len(sh.encoded(items[j].value))
		})

		lengths := make([]int, len(items))
		for j := range items {
			lengths[j] = len(sh.encoded(items[j].value))
		}

		var fresh arena.Arena
		projected := fresh.GrowthFor(lengths)

		s.memory.mu.Lock()
		if s.memory.max > 0 && s.memory.used-oldArena+projected > s.memory.max {
			s.memory.mu.Unlock()
			sh.mu.Unlock()
			continue
		}

		for j := range items {
			e := items[j].value
			value := sh.encoded(e)

			e.ref = fresh.Alloc(value)
			items[j].value = e
		}

		sh.arena = fresh

		for _, item := range items {
			sh.set(item.key, item.value)
		}
		sh.data.Compact()
		newArena, newIndex := sh.arena.TotalMemoryBytes(), sh.data.CapacityBytes()
		s.memory.used = s.memory.used - oldArena - oldIndex + newArena + newIndex
		s.memory.arenas = s.memory.arenas - oldArena + newArena
		s.memory.index = s.memory.index - oldIndex + newIndex
		s.memory.mu.Unlock()
		sh.mu.Unlock()
		compacted++
	}
	return compacted
}
