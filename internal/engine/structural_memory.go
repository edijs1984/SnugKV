package engine

import "unsafe"

// StructuralMemoryStats reports fixed per-store allocation payload that exists
// independently of keys and values. It intentionally excludes dynamic backing
// arrays, maps, allocator metadata, goroutine stacks, and other process RSS.
type StructuralMemoryStats struct {
	ShardStructBytes      uint64
	IndexTableStructBytes uint64
	PerShardBytes         uint64
	TotalBytes            uint64
	LegacyBaselineBytes   uint64
}

func structuralMemoryPerShard() uint64 {
	return uint64(unsafe.Sizeof(shard{}))
}

func structuralMemoryBytes(shards int) uint64 {
	return uint64(shards) * structuralMemoryPerShard()
}

// StructuralMemory measures the fixed shard backing array. The index.Table is
// embedded directly in each shard, so it no longer has a separate allocation.
// Dynamic index slots, entry pools, arena segments, free tables, expiration maps,
// and schemas are reported or accounted elsewhere.
func (s *Store) StructuralMemory() StructuralMemoryStats {
	shardBytes := uint64(unsafe.Sizeof(shard{}))
	count := uint64(len(s.shards))

	return StructuralMemoryStats{
		ShardStructBytes:      shardBytes,
		IndexTableStructBytes: 0,
		PerShardBytes:         shardBytes,
		TotalBytes:            count * shardBytes,
		LegacyBaselineBytes:   count * 512,
	}
}
