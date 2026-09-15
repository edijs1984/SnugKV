package arena

const freeTableBytes = uint64(freeBucketCount * 8)

// TotalMemoryBytes reports all heap storage owned by the arena, including the
// lazily allocated free-head table when it exists.
func (a *Arena) TotalMemoryBytes() uint64 {
	total := a.MemoryBytes()
	if a.free != nil {
		total += freeTableBytes
	}
	return total
}

// FreeGrowth reports the heap growth caused by freeing ref. The first real
// free in an arena lazily allocates the free-head table; later frees reuse it.
func (a *Arena) FreeGrowth(ref Ref) uint64 {
	if ref.generation == 0 || a.free != nil {
		return 0
	}
	return freeTableBytes
}
