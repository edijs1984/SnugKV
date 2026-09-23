package engine

import (
	"snugkv/internal/arena"
	"snugkv/internal/index"
	"sort"
	"unsafe"
)

func shardEntryStorageBytes(sh *shard) uint64 {
	bytes := uint64(cap(sh.entries)) * entryStructBytes
	if sh.metas != nil {
		bytes += uint64(unsafe.Sizeof(entryMetaSidecar{})) +
			uint64(cap(sh.metas.slots))*entryMetaSlotBytes
	}
	return bytes
}

// Compact reclaims unused arena segments, index slots, and dense entry
// over-capacity one shard at a time.
// It skips shards whose conservative transient-copy estimate exceeds scratch.
func (s *Store) Compact(scratch uint64) int {
	compacted := 0
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.Lock()
		oldArena, oldIndex := sh.arena.TotalMemoryBytes(), sh.data.CapacityBytes()
		oldEntries := shardEntryStorageBytes(sh)
		if oldArena+oldIndex+oldEntries == 0 ||
			(oldArena+oldIndex+oldEntries)*3 > scratch {
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
		storedLen := func(e entry) int {
			if e.ref.IsInline() {
				var buf [8]byte
				value, ok := e.ref.InlineInto(buf[:0])
				if !ok {
					panic("invalid inline reference")
				}
				return len(value)
			}
			return len(sh.encoded(e))
		}
		sort.Slice(items, func(i, j int) bool {
			return storedLen(items[i].value) > storedLen(items[j].value)
		})

		lengths := make([]int, 0, len(items))
		for j := range items {
			if !items[j].value.ref.IsInline() {
				lengths = append(lengths, storedLen(items[j].value))
			}
		}

		var fresh arena.Arena
		projected := fresh.GrowthFor(lengths)

		s.memory.mu.Lock()

		for j := range items {
			e := items[j].value
			if e.ref.IsInline() {
				var buf [8]byte
				value, ok := e.ref.InlineInto(buf[:0])
				if !ok {
					panic("invalid inline reference")
				}
				ref, ok := fresh.AllocInline(value)
				if !ok {
					panic("inline compaction invariant")
				}
				e.ref = ref
			} else {
				e.ref = fresh.Alloc(sh.encoded(e))
			}
			items[j].value = e
		}

		freshIndex := index.New[uint32]()
		freshEntries := make([]entryData, len(items))
		var freshMetas *entryMetaSidecar
		if sh.metas != nil {
			freshMetas = &entryMetaSidecar{slots: make([]*entryMeta, len(items))}
		}
		for j, item := range items {
			freshEntries[j] = item.value.entryData
			if freshMetas != nil {
				freshMetas.slots[j] = item.value.entryMeta
			}
			freshIndex.Set(item.key, uint32(j))
		}

		newArena := fresh.TotalMemoryBytes()
		newIndex := freshIndex.CapacityBytes()
		newEntries := uint64(cap(freshEntries)) * entryStructBytes
		if freshMetas != nil {
			newEntries += uint64(unsafe.Sizeof(entryMetaSidecar{})) +
				uint64(cap(freshMetas.slots))*entryMetaSlotBytes
		}

		// Account for the complete compacted shard, not only arena growth.
		// Dense entry holes and oversized index tables are reclaimed together.
		next := s.memory.used -
			oldArena - oldIndex - oldEntries +
			newArena + newIndex + newEntries
		if max := s.memory.max.Load(); max > 0 && next > max {
			s.memory.mu.Unlock()
			sh.mu.Unlock()
			continue
		}

		sh.arena = fresh
		sh.data = *freshIndex
		sh.entries = freshEntries
		sh.metas = freshMetas
		sh.freeIDs = nil

		s.memory.used = next
		s.memory.arenas = s.memory.arenas - oldArena + newArena
		s.memory.index = s.memory.index - oldIndex + newIndex
		s.memory.entries = s.memory.entries - oldEntries + newEntries
		s.memory.mu.Unlock()
		sh.mu.Unlock()
		compacted++
	}
	return compacted
}
