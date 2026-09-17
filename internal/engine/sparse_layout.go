package engine

// SparseLayoutStats exposes structural reservation data that is useful when
// diagnosing small/sparse datasets. It intentionally contains counts rather
// than another memory total; Memory() and Layout() remain the source of byte
// accounting.
type SparseLayoutStats struct {
	ShardCount        uint64
	ActiveShards      uint64
	EntrySlotsUsed    uint64
	EntryCapacity     uint64
	EntryFreeSlots    uint64
	ArenaSegments     uint64
	ArenaActiveShards uint64
}

func (s *Store) SparseLayout() SparseLayoutStats {
	stats := SparseLayoutStats{ShardCount: uint64(len(s.shards))}

	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.RLock()

		used := uint64(len(sh.entries) - len(sh.freeIDs))
		capacity := uint64(cap(sh.entries))
		segments := uint64(sh.arena.SegmentCount())

		stats.EntrySlotsUsed += used
		stats.EntryCapacity += capacity
		if capacity >= used {
			stats.EntryFreeSlots += capacity - used
		}
		if used > 0 {
			stats.ActiveShards++
		}
		if segments > 0 {
			stats.ArenaActiveShards++
			stats.ArenaSegments += segments
		}

		sh.mu.RUnlock()
	}

	return stats
}
