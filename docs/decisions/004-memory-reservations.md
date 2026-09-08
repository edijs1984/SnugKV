# ADR-004: Explicit storage reservations and cheap codecs

Status: Accepted

Charge explicit open-addressed index capacity, segmented arena capacity, key and
entry metadata, and fixed bounded schema/dictionary capacity. Writes reserve their
net change before publication, and MSET reserves its whole unique-key batch before
changing any entry. Compaction may release empty arena segments and index capacity.
The configured limit enforces these reservations, not process RSS.

The model deliberately labels bytes as accounted/reserved and does not report a
fabricated `used_memory_physical` value. Client buffers, goroutine stacks, and
temporary encoding candidates are outside the data budget and have separate network,
scratch, and optimizer limits. Raw mode remains available for baseline comparisons.

Cheap codecs produce immutable records. Integer, UUID, and timestamp candidates
must save payload bytes and reconstruct exactly before publication. Mutations and
representation changes use monotonic versions, including TTL changes and
delete/recreate, so optimizer compare-and-swap rejects stale work.

Validation covers OOM rollback for SET, GETSET, and MSET; index and arena growth and
compaction; expiration reclamation audits; encoded counters; and byte-exact reads.
