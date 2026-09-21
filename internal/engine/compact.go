package engine

import (
	"snugkv/internal/arena"
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

func compactDenseEntryStorage(sh *shard) {
	if len(sh.freeIDs) != 0 || cap(sh.entries) == len(sh.entries) {
		return
	}
	entries := make([]entryData, len(sh.entries))
	copy(entries, sh.entries)
	sh.entries = entries

	if sh.metas != nil {
		slots := make([]*entryMeta, len(sh.entries))
		copy(slots, sh.metas.slots)
		sh.metas.slots = slots
	}
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
		if max := s.memory.max.Load(); max > 0 && s.memory.used-oldArena+projected > max {
			s.memory.mu.Unlock()
			sh.mu.Unlock()
			continue
		}

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

		sh.arena = fresh

		for _, item := range items {
			sh.set(item.key, item.value)
		}
		sh.data.Compact()
		compactDenseEntryStorage(sh)
		newArena, newIndex := sh.arena.TotalMemoryBytes(), sh.data.CapacityBytes()
		newEntries := shardEntryStorageBytes(sh)
		s.memory.used = s.memory.used -
			oldArena - oldIndex - oldEntries +
			newArena + newIndex + newEntries
		s.memory.arenas = s.memory.arenas - oldArena + newArena
		s.memory.index = s.memory.index - oldIndex + newIndex
		s.memory.entries = s.memory.entries - oldEntries + newEntries
		s.memory.mu.Unlock()
		sh.mu.Unlock()
		compacted++
	}
	return compacted
}
