# ADR-007: Explicit-capacity open-addressed index

Status: Accepted

Replace Go maps in the raw key index with a generic open-addressed table. Slots
hold stable FNV-1a hash, full key, entry metadata and occupancy state. Full key
comparison resolves collisions. Linear probing and a maximum 70% load factor
provide simple bounded lookup probes; tombstones preserve probe chains and can
be reused. Slot-array capacity bytes are explicit and reserved before growth.

Readers and writers remain protected by shard locks. Growth temporarily holds
old and new arrays; this bounded transient allocation is distinct from steady
accounted capacity. Compaction rebuilds a shard under its write lock, never
exposing partially moved records. No unsafe memory access is used.

Tests force all keys to one hash, exercise deletion/reinsertion/compaction, and
compare projected allocation against actual slot capacity. Metadata remains a Go
struct. Payload ownership and generation-safe references are defined by ADR-008's
segmented arena.
