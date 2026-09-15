# In-memory value representations

SnugKV separates logical Redis-compatible values from their physical in-memory
representation. Scalar values may use adaptive codecs; HASH, SET, LIST, and ZSET
are native semantic types with dedicated packed formats. Native container formats
are excluded from the generic scalar optimizer.

## Common entry and index layout

The current common `entry` is 40 bytes on the supported amd64 build. Optional
activity/schema metadata lives in a sidecar and is normally absent for native
containers. The open-addressed key index stores `string key`, a compact value
handle, and slot state; `slot[uint32]` is 24 bytes. Index capacity grows in powers
of two and keeps live load at or below 80%.

Values are stored in generation-checked segmented byte arenas. Arena allocation
uses small exact/tight size classes plus geometric classes for larger values.
Current segments are 8 KiB. Entry/index/arena reservations are explicitly charged
to engine memory accounting.

Because index and entry reservations are shared by every datatype, tiny values can
be dominated by fixed per-key overhead. The 100k-key native-container benchmarks
currently show roughly 31.5 B/key of index reservation and 54–55 B/key of
entry/key accounting before payload. Sparse datasets can pay more because active
shards reserve initial entry/arena capacity.

## Scalar codecs

Raw scalar storage accepts arbitrary bytes. Optional encoding can select only an
exact, verified representation that is smaller than raw input.

Codec IDs currently include:

- `0`: raw;
- `1`: canonical signed integer;
- `3`: lowercase hyphenated UUID;
- `4`: UTC second-precision timestamp;
- `5`: exact JSON-shape representation;
- `6`: reserved for standalone dictionary values; dictionary IDs are currently nested in JSON-shape slots;
- `9`: LZ4;
- `10`: Zstandard.

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
