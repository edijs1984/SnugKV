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

		hotKeys := make([]string, 0)
		activeHot := false
		for key, e := range sh.all() {
			if !e.isHotHash() {
				continue
			}
			h, _, ok := sh.hotHashForKey(key)
			if !ok || h == nil {
				panic("HOT hash sidecar invariant")
			}
			if !s.hotHashIdle(h) {
				activeHot = true
				break
			}
			hotKeys = append(hotKeys, key)
		}
		if activeHot {
			sh.mu.Unlock()
			continue
		}
		for _, key := range hotKeys {
			e, ok := sh.get(key)
			if !ok {
				continue
			}
			if err := s.freezeHotHashLocked(sh, key, e); err != nil {
				panic(err)
			}
		}

		// Active hashes use indexed/HOT representations for cheap mutation.
		// Once maintenance sees them cold, collapse repeated field layouts into
		// shared shapes. Medium hashes may use either shaped format; large hashes
		// are compacted only when the fixed-width SF1 form is available, keeping
		// cold HGET O(1) instead of introducing a long variable-width scan.
		for key, e := range sh.all() {
			if e.valueType != TypeHash || e.isHotHash() {
				continue
			}
			physical := sh.encoded(e)
			if !isIndexedHash(physical) {
				continue
			}
			count, _, _, _, err := indexedHashMeta(physical)
			if err != nil || count < indexedHashPromoteFields {
				continue
			}
			pairs, err := decodeIndexedHash(physical)
			if err != nil {
				panic(err)
			}
			packed, err := encodePackedHash(pairs)
			if err != nil {
				panic(err)
			}
			updated := s.hashEntry(pairs, packed)
			if !isShapedHash(updated.data) {
				continue
			}
			if count >= hotHashPromoteFields && !isFixedShapedHash(updated.data) {
				continue
			}
			updated.expiresAt = sh.expirationAt(key, e)
			if err := s.publish(sh, key, updated); err != nil {
				panic(err)
			}
		}

		// Idle indexed lists carry append headroom (payload slack and a
		// power-of-two offset table). Trim it so the rebuilt arena stores them
		// at their exact size; a later RPUSH regrows the list on demand.
		listKeys := make([]string, 0)
		for key, e := range sh.all() {
			if e.valueType == TypeList && isIndexedList(sh.encoded(e)) {
				listKeys = append(listKeys, key)
			}
		}
		for _, key := range listKeys {
			e, ok := sh.get(key)
			if !ok {
				continue
			}
			physical := sh.encoded(e)
			best, changed := physical, false
			if trimmed, ok := trimIndexedList(physical); ok {
				best, changed = trimmed, true
			}
			// A list nobody writes needs neither its per-element offsets nor
			// append headroom. The cold layout is smaller still, but only worth
			// the conversion when it lands in a smaller arena block.
			if cold, ok := coldFromIndexedList(physical); ok &&
				arena.AllocationBytesForLength(len(cold)) < arena.AllocationBytesForLength(len(best)) {
				best, changed = cold, true
			}
			if !changed {
				continue
			}
			updated := preparedEntry{
				entry: entry{entryData: entryData{
					valueType: TypeList,
					rawLength: uint32(len(best)),
				}},
				data:      best,
				expiresAt: sh.expirationAt(key, e),
			}
			// Under memory pressure the replacement may not fit; the list
			// simply keeps its headroom until a later pass.
			_ = s.publish(sh, key, updated)
		}

		// Idle indexed sorted sets carry the same append headroom; trim it too.
		zsetKeys := make([]string, 0)
		for key, e := range sh.all() {
			if e.valueType == TypeZSet && isIndexedZSet(sh.encoded(e)) {
				zsetKeys = append(zsetKeys, key)
			}
		}
		for _, key := range zsetKeys {
			e, ok := sh.get(key)
			if !ok {
				continue
			}
			trimmed, ok := trimIndexedZSet(sh.encoded(e))
			if !ok {
				continue
			}
			updated := preparedEntry{
				entry: entry{entryData: entryData{
					valueType: TypeZSet,
					rawLength: uint32(len(trimmed)),
				}},
				data:      trimmed,
				expiresAt: sh.expirationAt(key, e),
			}
			_ = s.publish(sh, key, updated)
		}

		oldArena, oldIndex := sh.arena.TotalMemoryBytes(), sh.data.CapacityBytes()
		oldEntries := shardEntryStorageBytes(sh)
		oldHotSidecar := hotHashSidecarBytes(sh)
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
		_ = fresh.GrowthFor(lengths)

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
		// Size the rebuilt index to the live key count instead of letting
		// power-of-two growth leave it up to half empty.
		keyLogBytes := 0
		for _, item := range items {
			keyLogBytes += index.KeyRecordBytes(len(item.key))
		}
		freshIndex.ReserveKeys(len(items), keyLogBytes)
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
			oldArena - oldIndex - oldEntries - oldHotSidecar +
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
		s.memory.hotHashes -= oldHotSidecar
		s.memory.arenas = s.memory.arenas - oldArena + newArena
		s.memory.index = s.memory.index - oldIndex + newIndex
		s.memory.entries = s.memory.entries - oldEntries + newEntries
		s.memory.mu.Unlock()
		sh.mu.Unlock()
		compacted++
	}
	return compacted
}
