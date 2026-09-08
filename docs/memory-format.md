# In-memory value representations

The engine stores encoded values in generation-checked, power-of-two blocks within
8 KiB segmented byte arenas. Entry metadata retains the codec ID, original length,
arena reference, absolute expiration milliseconds, version, and bounded heat
counters. The record is immutable after publication; shard locks protect arena
views, and client reads return decoded copies.

IDs follow the specification: 0 raw, 1 canonical signed integer (Go signed varint,
zigzag), 3 lowercase hyphenated UUID (16 bytes), 4 UTC timestamp with exactly
second precision (`2006-01-02T15:04:05Z`, signed Unix seconds in little-endian 8 bytes).
ID 5 is exact JSON shape encoding, 6 is reserved for standalone dictionary values,
9 is LZ4, and 10 is Zstandard. IDs 2, 7–8, and 11–31 remain reserved; unknown IDs
fail decoding. Dictionary IDs are currently nested inside JSON shape slots.

The raw representation accepts all bytes. Noncanonical numerical strings, UUID
case variants, timestamp offsets and fractional formatting stay raw. A candidate
is selected only if smaller and exact reconstruction has been verified. Decoder
output length is checked against both the record and caller-supplied limit.
General decompression is limited to the stored original length. Registry
construction precedes concurrent use; registration is not a runtime API.

The open-addressed index stores a stable FNV-1a hash and the complete key, so hash
collisions cannot substitute another key. Capacity is explicit and uses a maximum
70% load factor. Keys remain Go strings with a conservative entry charge.

Memory reporting separates logical bytes, encoded payload bytes, arena capacity,
index capacity, entry/key charges, and bounded schema/dictionary reservations.
Arena and index compaction rebuild a shard under its write lock. Process RSS can
differ because of allocator state, goroutine stacks, code, persistence buffers,
network buffers, and benchmark-owned values.

Persistence uses `MCLOG001`, little-endian payload length, CRC32, and JSON logical
records containing base64 key/value bytes and absolute expiration milliseconds.
It stores logical state rather than this in-memory representation.
