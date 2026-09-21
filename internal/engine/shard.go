package engine

import (
	"snugkv/internal/arena"
	"snugkv/internal/codec/jsonshape"
	"snugkv/internal/index"
	"sync"
	"unsafe"
)

type entryMetaSidecar struct {
	slots []*entryMeta
}

type shard struct {
	sampleOffset int
	arena        arena.Arena
	mu           sync.RWMutex

	data    index.Table[uint32]
	entries []entryData
	metas   *entryMetaSidecar
	freeIDs []uint32

	expiration expirationQueue
	shapes     *jsonshape.Store
}

func (sh *shard) entryCapacityFor(additional int) int {
	if additional <= 0 {
		return cap(sh.entries)
	}

	needed := additional - len(sh.freeIDs)
	if needed <= 0 {
		return cap(sh.entries)
	}

	target := len(sh.entries) + needed
	capacity := cap(sh.entries)

	for target > capacity {
		// Sparse stores commonly have only a handful of keys in each of 256
		// shards. Start at four slots, then use a six-slot intermediate stage
		// before eight. This trims slack in the common 5-6 key/shard range while
		// adding an extra reallocation only for shards that grow past six keys.
		//
		// From eight slots onward, keep the established geometric ramp to 64.
		// Once a shard reaches 64 entries, restore the dense growth rule: grow
		// by 25%, but never by fewer than 64 slots.
		switch {
		case capacity == 0:
			capacity = 4
		case capacity == 4:
			capacity = 6
		case capacity == 6:
			capacity = 8
		case capacity < 64:
			capacity *= 2
			if capacity > 64 {
				capacity = 64
			}
		default:
			growth := capacity / 4
			if growth < 64 {
				growth = 64
			}
			capacity += growth
		}
	}

	return capacity
}

func (sh *shard) entryView(id uint32) entry {
	if int(id) >= len(sh.entries) {
		panic("invalid entry id")
	}
	var meta *entryMeta
	if sh.metas != nil {
		meta = sh.metas.slots[id]
	}
	return entry{entryData: sh.entries[id], entryMeta: meta}
}

func (sh *shard) ensureMetaSlots() {
	if sh.metas != nil {
		return
	}
	sh.metas = &entryMetaSidecar{
		slots: make([]*entryMeta, len(sh.entries), cap(sh.entries)),
	}
}

func (sh *shard) growMetaSlots(capacity int) {
	if sh.metas == nil || capacity <= cap(sh.metas.slots) {
		return
	}
	current := sh.metas.slots
	next := make([]*entryMeta, len(current), capacity)
	copy(next, current)
	sh.metas.slots = next
}

func (sh *shard) setMeta(id uint32, meta *entryMeta) {
	if meta != nil && sh.metas == nil {
		sh.ensureMetaSlots()
	}
	if sh.metas != nil {
		sh.metas.slots[id] = meta
	}
}

func (sh *shard) metaSlotGrowthBytes(additional int, needMeta bool) uint64 {
	nextEntryCap := sh.entryCapacityFor(additional)
	if sh.metas == nil {
		if !needMeta {
			return 0
		}
		return uint64(unsafe.Sizeof(entryMetaSidecar{})) +
			uint64(nextEntryCap)*uint64(unsafe.Sizeof((*entryMeta)(nil)))
	}
	if nextEntryCap <= cap(sh.metas.slots) {
		return 0
	}
	return uint64(nextEntryCap-cap(sh.metas.slots)) * uint64(unsafe.Sizeof((*entryMeta)(nil)))
}

func (sh *shard) get(key string) (entry, bool) {
	return sh.getHashed(key, index.Hash(key))
}

func (sh *shard) getHashed(key string, hash uint64) (entry, bool) {
	id, ok := sh.data.GetHashed(key, hash)
	if !ok {
		return entry{}, false
	}

	return sh.entryView(id), true
}

func (sh *shard) getHashedBytes(key []byte, hash uint64) (entry, bool) {
	id, ok := sh.data.GetHashedBytes(key, hash)
	if !ok {
		return entry{}, false
	}

	return sh.entryView(id), true
}

func (sh *shard) set(key string, e entry) {
	if id, ok := sh.data.Get(key); ok {
		sh.entries[id] = e.entryData
		sh.setMeta(id, e.entryMeta)
		return
	}
	sh.insertEntry(key, index.Hash(key), e, false)
}

func (sh *shard) setKnownHashed(key string, hash uint64, e entry, exists bool) {
	if exists {
		id, ok := sh.data.GetHashed(key, hash)
		if !ok {
			panic("known shard entry is missing")
		}
		sh.entries[id] = e.entryData
		sh.setMeta(id, e.entryMeta)
		return
	}
	sh.insertEntry(key, hash, e, true)
}

func (sh *shard) insertEntry(key string, hash uint64, e entry, hashKnown bool) {
	var id uint32
	if n := len(sh.freeIDs); n > 0 {
		id = sh.freeIDs[n-1]
		sh.freeIDs = sh.freeIDs[:n-1]
		sh.entries[id] = e.entryData
		sh.setMeta(id, e.entryMeta)
	} else {
		id = uint32(len(sh.entries))
		if len(sh.entries) == cap(sh.entries) {
			next := sh.entryCapacityFor(1)
			entries := make([]entryData, len(sh.entries), next)
			copy(entries, sh.entries)
			sh.entries = entries
			sh.growMetaSlots(next)
		}
		sh.entries = append(sh.entries, e.entryData)
		if sh.metas != nil {
				sh.metas.slots = append(sh.metas.slots, nil)
		}
		sh.setMeta(id, e.entryMeta)
	}
	if hashKnown {
		sh.data.SetKnownHashed(key, id, hash, false)
	} else {
		sh.data.Set(key, id)
	}
}

func (sh *shard) delete(key string) bool {
	id, ok := sh.data.Get(key)
	if !ok {
		return false
	}

	sh.data.Delete(key)

	if int(id) >= len(sh.entries) {
		panic("invalid entry id")
	}

	sh.entries[id] = entryData{}
	if sh.metas != nil {
		sh.metas.slots[id] = nil
	}
	sh.freeIDs = append(sh.freeIDs, id)

	return true
}

func (sh *shard) all() func(func(string, entry) bool) {
	return func(yield func(string, entry) bool) {
		for key, id := range sh.data.All() {
			if int(id) >= len(sh.entries) {
				panic("invalid entry id")
			}

			if !yield(key, sh.entryView(id)) {
				return
			}
		}
	}
}

func (s *Store) shardFor(key string) *shard {
	return s.shardForHash(index.Hash(key))
}

func (s *Store) shardForHash(hash uint64) *shard {
	return &s.shards[(hash>>32)&uint64(len(s.shards)-1)]
}
