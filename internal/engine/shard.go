package engine

import (
	"morphcache/internal/arena"
	"morphcache/internal/codec/jsonshape"
	"morphcache/internal/index"
	"sync"
)

type shard struct {
	sampleOffset int
	arena        arena.Arena
	mu           sync.RWMutex
	data         *index.Table[entry]
	expiration   expirationQueue
	shapes       *jsonshape.Store
}

func (s *Store) shardFor(key string) *shard {
	// Stable FNV-1a; the map still compares complete keys.
	h := uint64(14695981039346656037)
	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= 1099511628211
	}
	return &s.shards[h&uint64(len(s.shards)-1)]
}
