package engine

import (
	"snugkv/internal/index"
	"unsafe"
)

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
	var table index.Table[uint32]
	return uint64(unsafe.Sizeof(shard{})) + uint64(unsafe.Sizeof(table))
}

func structuralMemoryBytes(shards int) uint64 {
	return uint64(shards) * structuralMemoryPerShard()
}

// StructuralMemory measures the fixed shard backing array plus the separately
// allocated index.Table object owned by every shard. Dynamic index slots, entry
// pools, arena segments, free tables, expiration maps, and schemas are reported
// or accounted elsewhere.
func (s *Store) StructuralMemory() StructuralMemoryStats {
	var table index.Table[uint32]

	shardBytes := uint64(unsafe.Sizeof(shard{}))
	tableBytes := uint64(unsafe.Sizeof(table))
	perShard := shardBytes + tableBytes
	count := uint64(len(s.shards))

	return StructuralMemoryStats{
		ShardStructBytes:      shardBytes,
		IndexTableStructBytes: tableBytes,
		PerShardBytes:         perShard,
		TotalBytes:            count * perShard,
		LegacyBaselineBytes:   count * 512,
	}
}
