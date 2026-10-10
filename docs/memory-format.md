# In-memory value representations

SnugKV separates logical Redis-compatible values from their physical in-memory
representation. Scalar values may use adaptive codecs; HASH, SET, LIST, and ZSET
are native semantic types with dedicated packed formats. Native container formats
are excluded from the generic scalar optimizer.

## Common entry and index layout

The stored hot-path entry is 16 bytes on the supported amd64 build. Two words
hold the arena reference's location (or an inline payload up to 8 bytes) and, in
the second word, the logical length (32 bits), codec ID (6), value type (5),
expiry and raw-stable flags, and the arena generation (19 bits). Command code
works with an unpacked `entryData` view; only the per-shard array is packed.
Optional activity/schema metadata is not embedded in every entry:
each shard allocates a metadata sidecar lazily only when a stored value actually
needs one. Metadata-free workloads therefore pay no per-entry nil metadata
pointer.

The open-addressed key index uses one 8-byte word per slot and holds no
pointers. A live slot packs a 30-bit offset into the shard's key log, a 27-bit
entry ID and a 6-bit hash fingerprint; the fingerprint rejects most probe
candidates before any key bytes are read, and exact key comparison still makes
hash collisions safe. Keys are stored once, in an append-only per-shard key log,
as a varint length followed by the key bytes, so a key costs its length plus one
byte and no separate heap object. Log records are never overwritten: growth and
repacking copy into a new array, which keeps key strings already handed to
callers valid. Deleted keys leave dead records that repacking reclaims, and the
index repacks when they exceed a third of the log. A shard holds at most
2^27 - 1 entries and 1 GiB of key data. Index capacity grows gradually and uses
the existing bounded load-factor policy.

Values are normally stored in generation-checked segmented byte arenas. Arena
allocation uses small exact/tight size classes plus geometric classes for larger
values, with 8 KiB segments. Tiny encoded integer/unsigned/float/timestamp payloads
of up to 8 bytes can instead be stored inline in the existing `arena.Ref`; this
preserves generation/version semantics while eliminating arena payload and block
reservation for those values. Dense entry-array over-capacity can be reclaimed by
compaction without changing entry IDs.

Entry/index/arena reservations, lazy metadata-sidecar storage, and live key bytes
are explicitly charged to engine memory accounting. Sparse datasets can still pay
more because active shards reserve initial index/entry capacity.

## Scalar codecs

Raw scalar storage accepts arbitrary bytes. Optional encoding can select only an
exact, verified representation that is smaller than raw input.

Codec IDs currently include:

- `0`: raw;
- `1`: canonical signed integer;
- `2`: canonical unsigned integer;
- `3`: lowercase hyphenated UUID;
- `4`: UTC second-precision timestamp;
- `5`: exact JSON-shape representation;
- `6`: canonical float;
- `7`: boolean;
- `8`: repeated-byte encoding;
- `9`: LZ4;
- `10`: Zstandard;
- `11`: periodic/repeating-pattern encoding.

Unknown codec IDs fail decoding. Decoder output is length-bounded and every
selected candidate is reconstructed and compared before publication.

## Native HASH

Logical HASH values use canonical SH1 encoding: a versioned header followed by
sorted binary-safe field/value pairs with varint lengths.

A physical SH2 representation can reference a bounded in-memory shared field-shape
catalog when repeated field layouts save enough memory. SH2 is an in-memory
optimization only; export/persistence reconstructs canonical SH1. Tiny or unique
hashes remain SH1. HASH mutations preserve TTL while the key survives.

## Native SET

Canonical SET state is SS1: a versioned header plus sorted unique binary-safe
members with varint lengths.

Physical storage is adaptive:

- `singleton`: one-member raw physical form when safe;
- `prefix`: compact fixed/structured member representation using front coding;
- `packed`: canonical SS1 fallback.

Logical persistence exports SS1 regardless of physical representation. SET
mutations preserve TTL while the key survives.

## Native LIST

LIST uses canonical SL1 storage: a versioned header, element count, then ordered
binary-safe elements with varint lengths. Duplicates and order are preserved.

SL1 is intentionally the only v1 physical LIST representation. Benchmarks showed
that small-list memory deficits are dominated by shared per-key overhead rather
than list payload, so no datatype-specific adaptive LIST format is planned unless
new evidence changes that conclusion.

## Native ZSET

ZSET values are always sorted by `(score, member)` and use versioned SZ formats.
The decoder remains compatible with earlier physical versions.

Current adaptive choices are:

- raw float64 scores when integer-delta encoding is not applicable or not smaller;
- exact int64 score encoding with delta varints when the full score stream is
  smaller than raw float64;
- raw member lengths/bytes when front coding is not smaller;
- member prefix/front coding when the complete encoded representation shrinks.

The current adaptive header can combine integer score deltas and member front
coding. Fractional scores, infinities, dispersed members, or large integer gaps
fall back automatically rather than paying a larger representation.

ZSET deliberately has no permanent skiplist/tree or member hash index in v1.
Member lookup/update and many queries decode the packed representation and operate
in memory temporarily. Lex commands build a temporary lexicographic view instead
of retaining a second index.

## Persistence boundary

Persistence stores logical state rather than depending on transient physical
optimizations. Native container export validates/reconstructs their canonical
logical representation before writing persistence records. In-memory shape IDs
and adaptive physical choices therefore do not become durable compatibility
requirements.

AOF/snapshot frames use checksummed logical records with key/value bytes and
expiration metadata. Restart tests cover native datatype recovery, truncated final
AOF frames, and checksum-corruption rejection.

## Memory reporting

`SNUG.STATS`, `INFO memory`, and benchmark layout statistics separate key/index,
entry reservation, arena reservation, live allocation, payload, schema, and
metadata accounting. Engine-accounted memory is not process RSS; Go runtime state,
network buffers, stacks, persistence buffers, allocator state, and benchmark-owned
objects can make RSS materially different.
