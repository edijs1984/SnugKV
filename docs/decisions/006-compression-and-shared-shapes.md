# ADR-006: Background compression and bounded shared JSON shapes

Status: Accepted

Use github.com/pierrec/lz4/v4 v4.1.22 for LZ4 blocks (codec 9), and
 github.com/klauspost/compress v1.18.4 for Zstandard frames (codec 10). The standard
library provides neither format; these dependencies implement the formats named
by the specification. Versions and checksums are pinned in go.mod/go.sum.
Upstream APIs: https://github.com/pierrec/lz4 and https://github.com/klauspost/compress.

General compression is restricted to background candidates and verified against
original bytes before publication. Warm values may use LZ4; cold values may use
Zstandard. Hot and write-heavy values avoid general compression. Decoder output
is bounded by the stored raw length. Runtime enablement is explicit.

JSON shape codec 5 preserves literal spans and length-prefixes raw primitive
slots. It validates JSON without reserializing it. Templates are local to shards,
identified by complete serialized literal spans (no hash-only identity). Admission
uses a fixed frequency sketch and threshold 8. Each enabled shard reserves 64 KiB
for schema storage plus 4 KiB for admission counters. Reference counts are acquired
at publication and released on replacement/removal. Candidate admission can change
while encoding; a commit whose schema cannot be retained is rejected safely.

Initial slots remain raw. Typed slot packing and dictionary slots are separate
extensions. Rewrite hysteresis requires 16 bytes and 12.5% improvement, and a
minimum residency interval. Internal rewrites advance the CAS version and retain
logical TTL/access metadata. Codec enabling and schema budgets require restart.
