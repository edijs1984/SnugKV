package engine

import (
	"snugkv/internal/arena"
	"snugkv/internal/codec/jsonshape"
	"snugkv/internal/index"
	"sync"
)

type shard struct {
	sampleOffset int
	arena        arena.Arena
	mu           sync.RWMutex

	data    *index.Table[uint32]
	entries []entry
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
		// shards. Ramp tiny shards geometrically so a first insertion reserves
		// only four 40-byte entries instead of eight or 64.
		//
		// Once a shard reaches 64 entries, restore the established dense growth
		// rule: grow by 25%, but never by fewer than 64 slots. The smaller +16
		// floor used by the first sparse-memory pass caused many extra
		// reallocations and left ~15k more reserved entries at 100k keys.
		switch {
		case capacity == 0:
			capacity = 4
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

func (sh *shard) get(key string) (entry, bool) {
	id, ok := sh.data.Get(key)
	if !ok {
		return entry{}, false
	}

	if int(id) >= len(sh.entries) {
		panic("invalid entry id")
	}

	return sh.entries[id], true
}

func (sh *shard) set(key string, e entry) {
	if id, ok := sh.data.Get(key); ok {
		sh.entries[id] = e
		return
	}

	var id uint32

	if n := len(sh.freeIDs); n > 0 {
		id = sh.freeIDs[n-1]
		sh.freeIDs = sh.freeIDs[:n-1]
		sh.entries[id] = e
	} else {
		id = uint32(len(sh.entries))

		if len(sh.entries) == cap(sh.entries) {
			next := sh.entryCapacityFor(1)

			entries := make([]entry, len(sh.entries), next)
			copy(entries, sh.entries)
			sh.entries = entries
		}

		sh.entries = append(sh.entries, e)
	}

	sh.data.Set(key, id)
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

	sh.entries[id] = entry{}
	sh.freeIDs = append(sh.freeIDs, id)

	return true
}

func (sh *shard) all() func(func(string, entry) bool) {
	return func(yield func(string, entry) bool) {
		for key, id := range sh.data.All() {
			if int(id) >= len(sh.entries) {
				panic("invalid entry id")
			}

			if !yield(key, sh.entries[id]) {
				return
			}
		}
	}
}

func (s *Store) shardFor(key string) *shard {
	h := uint64(14695981039346656037)

	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= 1099511628211
	}

	return &s.shards[h&uint64(len(s.shards)-1)]
}
