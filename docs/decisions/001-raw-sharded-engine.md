# ADR-001: Raw sharded engine baseline

Status: Superseded by ADR-007 and ADR-008

This ADR records the initial correctness baseline. The final engine retains
power-of-two shards, FNV-1a routing, full-key collision checks, shard locks, and
immutable client-visible byte ownership. ADR-007 replaces the Go map with an
explicit-capacity open-addressed index, and ADR-008 moves payloads into segmented
arenas.

Expired reads return missing without unsafe lock upgrades. Mutations remove expired
entries under the shard write lock. Active cleanup uses indexed expiration heaps
and bounded batches. Clock access remains injectable for deterministic tests.

Logical key/value reporting excludes expired entries. The final accounted-memory
model tracks explicit index and arena capacity plus live entry and bounded schema
reservations. It is an engine budget rather than process RSS.

Validation includes fake-clock expiration, concurrent overwrite/read and counter
tests, ownership tests, collision tests, memory audits, race detection, and engine
benchmarks.
