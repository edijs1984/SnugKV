# ADR-008: Segmented value arena

Status: Accepted

Values use 64 KiB byte segments and power-of-two blocks, including an eight-byte
generation header. Larger values receive larger power-of-two segments. Freed
blocks form intrusive free lists inside their former payload bytes; freeing does
not allocate bookkeeping. Segment capacity and segment-table capacity are explicit.

References carry segment, offset, logical encoded length and generation. View
checks generation and bounds. Shard locks protect readers throughout decoding;
returned client data is copied. Publication allocates new storage before retiring
the old reference. Batch planning computes required growth before acknowledging
writes. Segment metadata is reserved conservatively at 32 bytes per slot.

Arena compaction rebuilds a shard into a new arena under its write lock, updates
all references atomically, and releases the old arena after readers have drained.
Both arenas coexist temporarily, so compaction needs a bounded scratch allowance.
Key strings remain in the index and carry a separate key/allocator reservation.
