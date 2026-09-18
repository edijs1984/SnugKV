# SnugKV

## Foundational Product and Engineering Specification

**Status:** Historical/foundational specification; current implementation status is tracked in `README.md`, `COMPATIBILITY.md`, `PLAN.md`, and `PROGRESS.md`  
**Implementation language:** Go  
**Product category:** Redis-compatible, memory-efficient in-memory data store  
**Primary differentiator:** Transparent, reversible and workload-aware value encoding  
**Working name:** SnugKV; the product may be renamed later

---

## 1. Implementation guidance

This file records the original product constraints, storage invariants, and architectural direction that shaped SnugKV. It is no longer the authoritative command roadmap: the implementation has intentionally grown well beyond the original version-0.1 surface.

Before editing code, use the current repository state plus `COMPATIBILITY.md`, `PLAN.md`, `PROGRESS.md`, and the focused documents under `docs/` as the source of truth for implemented commands, compatibility status, and next work. Where this historical specification conflicts with those current sources, the current sources win.

Agents must follow these rules:

1. Implement the milestones in order unless the repository already records an approved deviation.
2. Prefer correctness and measurable behavior over speculative optimization.
3. Preserve byte-for-byte compatibility for ordinary Redis-style `SET` and `GET` operations.
4. Never claim a memory or performance improvement without a reproducible benchmark.
5. Keep public behavior independent from the internal encoding. A client must not need to know whether a value is raw, packed, dictionary encoded or compressed.
6. Keep every codec deterministic and reversible.
7. Add tests with each behavior change. Codec work requires property tests and fuzz tests.
8. Run at minimum:

   ```bash
   go fmt ./...
   go vet ./...
   go test ./...
   go test -race ./...
   ```

9. Do not introduce an external dependency when the standard library is sufficient. When a dependency is justified, document the reason in an ADR.
10. Do not add clustering, distributed consensus or broad Redis compatibility before the single-node engine satisfies its acceptance criteria.
11. Keep changes small enough to review. A milestone should be split into independently testable vertical slices.
12. Record major technical decisions in `docs/decisions/` as Architecture Decision Records.
13. Maintain `PROGRESS.md` with completed items, current work, known failures and the next recommended task.
14. If this specification is ambiguous, choose the smallest design that preserves the stated invariants and record the interpretation in an ADR.

### Definition of an acceptable agent contribution

A contribution is complete only when:

- the code compiles;
- relevant tests pass;
- the race detector passes for concurrency changes;
- new public behavior is documented;
- memory ownership is clear;
- failure paths are tested;
- benchmarks are added for performance-sensitive changes;
- no unverified performance claim is added to documentation.

---

## 2. Executive summary

SnugKV is a single-node in-memory key-value database written in Go. It exposes a deliberately small Redis-compatible interface while storing values in more compact representations whenever doing so is safe and beneficial.

Applications frequently cache values in inefficient textual forms:

- integers represented by decimal strings;
- UUIDs represented by 36-byte text;
- timestamps represented by ISO strings;
- repeated JSON property names stored in every record;
- repeated enum and status values;
- sorted numerical sequences stored as full-width numbers;
- sparse integer sets stored as general-purpose collections;
- cold values kept uncompressed even when they are rarely read.

SnugKV detects eligible patterns, evaluates candidate codecs and stores a compact representation. On `GET`, it reconstructs the exact original byte sequence. The client continues to see normal Redis-like values.

The system adapts to workload behavior:

- frequently read values prefer low-latency packed representations;
- frequently modified values avoid expensive recompression;
- warm values may use fast compression;
- cold values may use denser compression;
- structurally similar JSON records may share schemas and dictionaries;
- values may be re-encoded in the background as their access patterns change.

The initial product is not intended to replace every Redis feature. It is intended to prove that a carefully designed adaptive store can hold significantly more real application data per gigabyte without forcing application changes.

---

## 3. Problem statement

In-memory databases trade RAM for speed. In many deployments, the stored payload is only part of the total cost. Memory is also consumed by:

- repeated keys and field names;
- general-purpose object representations;
- pointers and allocator metadata;
- hash-table capacity and fragmentation;
- per-entry expiration data;
- duplicated strings;
- cold values that remain in their fastest but largest form;
- garbage-collector metadata and object scanning.

Compression alone does not solve the complete problem. General compression can waste CPU on small, hot or frequently modified values. It can also increase latency and temporarily allocate large decode buffers.

SnugKV therefore needs a policy engine rather than one universal codec. It must decide:

1. whether a value should be transformed;
2. which lossless representation best suits that value;
3. whether the memory saving justifies encoding and decoding cost;
4. whether the representation should change as the value becomes hotter, colder or more frequently modified;
5. how to perform that change without blocking clients or overwriting newer data.

---

## 4. Product goals

### 4.1 Primary goals

1. Provide a useful Redis-compatible subset for common cache and session workloads.
2. Return exactly the bytes supplied to `SET` when the value is read through `GET`.
3. Automatically reduce memory for supported data patterns.
4. Make all encoding decisions observable and explainable.
5. Bound CPU spent on optimization.
6. Remain correct under concurrent access, retries, expiration, eviction and background rewriting.
7. Offer reproducible comparisons against a raw-storage baseline and Redis.
8. Keep the architecture modular so codecs and policies can evolve independently.

### 4.2 Secondary goals

1. Support optional append-only persistence and snapshots after the in-memory engine is stable.
2. Provide administrative commands for inspecting memory usage and encoding decisions.
3. Provide Prometheus-compatible operational metrics.
4. Allow operators to disable individual codecs or automatic re-encoding.

### 4.3 Long-term scope boundaries

The implementation has exceeded several original v0.1 non-goals, including Lua
scripting, Redis Functions, Streams, Pub/Sub, transactions, CONFIG/COMMAND
tooling, and AUTH/ACL support. Current boundaries are:

- full Redis command compatibility;
- Redis Cluster compatibility;
- multi-node replication or automatic failover;
- arbitrary Redis modules;
- distributed transactions across nodes;
- multi-database support beyond database `0`;
- a general query language;
- complete RedisJSON-compatible document mutation semantics;
- semantic modification of values passed through ordinary `SET`;
- persistent storage competitive with dedicated disk databases;
- claiming a fixed compression ratio for all workloads.

---

## 5. Core invariants

The following invariants must hold throughout the implementation:

### 5.1 Exact round-trip

For every accepted byte sequence `v`:

```text
SET k v
GET k == v
```

Equality is byte-for-byte. Whitespace, JSON key ordering, Unicode bytes, case, numerical formatting and trailing newlines must be preserved.

### 5.2 No stale rewrite

A background optimizer must never replace a value that has changed since the optimizer read it.

### 5.3 Atomic visibility

A reader sees either the old complete value or the new complete value, never a partial record.

### 5.4 Expiration correctness

Once a key is logically expired, client commands must behave as though it does not exist, even if its physical memory has not yet been reclaimed.

### 5.5 Memory limit

When a configured hard memory limit is active, acknowledged writes must not push accounted memory indefinitely beyond that limit. Temporary bounded encoding buffers are permitted, but their maximum budget must be configured and observed.

### 5.6 Reversible codecs

Every successful encoding must decode to the original bytes. A codec failure must fall back to the original representation rather than corrupt or reject otherwise valid data.

### 5.7 Stable behavior across restart

When persistence is enabled, codec IDs and persisted record formats must be versioned. Existing IDs must never be reassigned to different formats.

---

## 6. Target workloads

SnugKV is initially optimized for:

1. **Session records** containing repeated JSON keys, UUIDs, timestamps, booleans, country codes and plan/status enums.
2. **API response caches** containing many objects with the same shape but different values.
3. **IoT and industrial telemetry** containing timestamps, equipment identifiers, states and numerical sequences.
4. **User and authorization metadata** containing repeated roles, permissions and status values.
5. **Counters and rate-limit values** stored as canonical integers.
6. **Large collections of identifiers** that can use compact binary or bitmap representations.
7. **Mixed hot and cold cache populations** where cold values can tolerate a higher decode cost.

SnugKV must also behave sensibly for incompressible values. Random or already compressed bytes must remain raw and should incur only bounded metadata overhead.

---

## 7. User-facing model

### 7.1 Compatibility promise

SnugKV targets RESP2 and a broad single-node Redis-compatible command surface. Existing Redis clients should be able to connect for supported operations without a custom SDK. The current supported surface is maintained in `README.md` and `COMPATIBILITY.md`.

Compatibility is behavioral only for explicitly supported commands. Unsupported commands return a clear error and must never silently approximate different semantics.

### 7.2 Historical minimum command baseline

The list below is the original minimum baseline, not the current complete command
surface. The implementation now includes native HASH/SET/LIST/ZSET/STREAM,
Pub/Sub, transactions, scripting/Functions, HyperLogLog, GEO, SORT/COPY,
COMMAND/CONFIG tooling, CLIENT management, and AUTH/ACL support.

#### Connection and diagnostics

- `PING [message]`
- `ECHO message`
- `QUIT`
- `SELECT 0`
- `HELLO 2`
- `INFO [section]`
- `DBSIZE`
- `COMMAND` with enough metadata for supported commands

#### String operations

- `SET key value [NX|XX] [EX seconds|PX milliseconds]`
- `GET key`
- `MGET key [key ...]`
- `DEL key [key ...]`
- `EXISTS key [key ...]`
- `GETSET key value`
- `SETNX key value`
- `MSET key value [key value ...]`
- `INCR key`
- `INCRBY key increment`
- `DECR key`
- `DECRBY key decrement`
- `STRLEN key`

#### Expiration

- `EXPIRE key seconds`
- `PEXPIRE key milliseconds`
- `TTL key`
- `PTTL key`
- `PERSIST key`

#### Administrative extensions

- `SNUG.ENCODING key`
- `SNUG.MEMORY key`
- `SNUG.STATS`
- `SNUG.COMPACT [key|ALL]`
- `SNUG.POLICY key`

Administrative extensions must use a namespace that does not collide with Redis commands.

### 7.3 Exact-byte behavior and typed optimizations

Ordinary string commands remain byte-oriented. Internal numerical or structural codecs are allowed only when decoding reconstructs the exact original bytes.

Examples:

- `"42"` may use an integer codec.
- `"0042"` must remain raw unless the codec preserves the leading zeros.
- `"-0"` must not be normalized to `"0"`.
- a lowercase UUID must not be returned in uppercase.
- JSON whitespace and property order must not change.

Future typed commands may offer semantic JSON behavior, but they are outside version 1.

---

## 8. High-level architecture

```text
                         +----------------------+
Redis client ----------> | RESP TCP server      |
                         +----------+-----------+
                                    |
                                    v
                         +----------------------+
                         | Command dispatcher   |
                         +----------+-----------+
                                    |
                   hash(key)        v
                         +----------------------+
                         | Sharded engine       |
                         | index + entry metadata|
                         +----+------------+----+
                              |            |
                              v            v
                     +-------------+  +----------------+
                     | Byte arenas |  | TTL / eviction |
                     +------+------+  +----------------+
                            |
                            v
                   +-------------------+
                   | Codec registry    |
                   | raw/packed/JSON/  |
                   | LZ4/Zstd/bitmap   |
                   +---------+---------+
                             ^
                             |
                   +-------------------+
                   | Adaptive optimizer|
                   | heat + cost policy|
                   +-------------------+

Optional durability path:

Command dispatcher ---> AOF writer ---> fsync policy
Storage engine -------> snapshot writer
```

### 8.1 Major components

1. **RESP server:** parses requests, enforces limits and serializes replies.
2. **Command dispatcher:** validates command semantics and routes keys to shards.
3. **Sharded engine:** provides atomic operations and owns key/value lifecycle.
4. **Index:** maps key hashes to key and entry locations.
5. **Arenas:** store keys and encoded values in contiguous byte regions.
6. **Codec registry:** exposes deterministic codecs behind a common interface.
7. **Policy engine:** selects encodings based on real byte cost and workload heat.
8. **Background optimizer:** safely re-encodes eligible values.
9. **TTL manager:** provides logical expiration and physical cleanup.
10. **Eviction manager:** enforces memory policy when configured.
11. **Persistence:** optional AOF and snapshots.
12. **Observability:** metrics, structured logs and administrative commands.

---

## 9. Repository structure

Agents should start with the following structure and modify it only through an ADR:

```text
.
├── cmd/
│   ├── snugkv/              # server executable
│   └── snugbench/              # benchmark/data generator CLI
├── internal/
│   ├── arena/                   # segmented byte allocation and compaction
│   ├── codec/
│   │   ├── raw/
│   │   ├── integer/
│   │   ├── uuid/
│   │   ├── timestamp/
│   │   ├── jsonshape/
│   │   ├── dictionary/
│   │   ├── delta/
│   │   ├── bitmap/
│   │   ├── lz4codec/
│   │   └── zstdcodec/
│   ├── config/
│   ├── engine/
│   ├── eviction/
│   ├── index/
│   ├── memory/
│   ├── optimizer/
│   ├── persistence/
│   ├── resp/
│   ├── server/
│   ├── stats/
│   └── ttl/
├── pkg/
│   └── embedded/                # optional supported embedding API
├── docs/
│   ├── decisions/
│   ├── protocol.md
│   ├── memory-format.md
│   └── operations.md
├── testdata/
│   ├── sessions/
│   ├── api-responses/
│   ├── telemetry/
│   └── adversarial/
├── benchmarks/
├── scripts/
├── Dockerfile
├── docker-compose.yml
├── Makefile
├── PROGRESS.md
├── README.md
└── go.mod
```

Packages under `internal/` must not import from `cmd/`. The storage engine must not depend on RESP. Protocol values must be translated to engine operations at the dispatcher boundary.

---

## 10. Storage engine

### 10.1 Sharding

The keyspace is divided into a power-of-two number of shards. The initial default is 256, configurable before startup.

```text
shard = hash(key) & (shardCount - 1)
```

Each shard owns:

- its key index;
- entry metadata;
- key and value arena references;
- local schema and string dictionaries;
- TTL scheduling state;
- memory counters;
- local optimizer sampling state;
- an `RWMutex` for version 1 correctness.

Global state should be limited to configuration, aggregate metrics, persistence coordination and lifecycle management.

### 10.2 Hashing and collision handling

Use a fast, stable 64-bit non-cryptographic hash. The index must retain enough key information to verify equality after a hash match. A hash collision must never return or modify the wrong key.

Do not use the hash as the identity of a key.

### 10.3 Index evolution

Implement behind an interface so the first correct version can use Go maps while a later milestone replaces them with an open-addressed packed index.

Recommended interface:

```go
type Index interface {
    Find(hash uint64, key []byte) (EntryID, bool)
    Insert(hash uint64, key []byte, id EntryID) error
    Delete(hash uint64, key []byte) (EntryID, bool)
    Len() int
    CapacityBytes() uint64
    Iterate(func(hash uint64, id EntryID) bool)
}
```

The benchmark baseline must measure both the initial index and the packed index so savings are attributable.

### 10.4 Entry metadata

Entry metadata should be fixed-size or tightly packed. Exact field widths may change after measurement, but the logical model is:

```go
type Entry struct {
    KeyRef       Ref
    ValueRef     Ref
    ExpiresAtMS  int64
    Version      uint64
    LastAccess   uint32
    ReadCount    uint16
    WriteCount   uint16
    CodecID      uint8
    Flags        uint8
    RawLength    uint32
}
```

Do not keep a heap pointer per field. Prefer offsets and lengths into byte arenas.

`Version` increments for every logical mutation, including overwrite, expiration change and deletion/recreation. A background rewrite may commit only if the version it observed remains current.

### 10.5 Byte arenas

Keys and values should eventually live in segmented `[]byte` arenas rather than separate Go objects. Byte slices are not pointer-scanned element by element, reducing garbage-collector work.

Recommended properties:

- fixed-size or size-classed segments;
- references expressed as segment ID, offset and length;
- free lists for reusable blocks;
- explicit accounting for live bytes and fragmentation;
- immutable value bytes after publication;
- background segment compaction only after correctness is proven;
- generation or lifetime protection so readers never access reused storage.

An initial implementation may hold the shard read lock while decoding a value. A future epoch-based or generation-pinning design may allow lock-free decoding. Do not introduce unsafe memory access in version 1.

### 10.6 Memory accounting

Account at least:

- key bytes;
- value payload bytes;
- arena unused capacity;
- index allocation;
- entry metadata;
- schema bytes;
- dictionary bytes;
- TTL structures;
- persistence buffers;
- codec scratch budget;
- reclaimable fragmentation.

The accounting model must distinguish logical dataset size from physical resident allocation.

---

## 11. Codec system

### 11.1 Required interface

All codecs implement a common interface similar to:

```go
type ID uint8

type Context struct {
    SchemaStore     SchemaStore
    Dictionary     Dictionary
    Scratch         ScratchAllocator
    MaxOutputBytes  int
}

type Estimate struct {
    Eligible          bool
    EstimatedBytes    int
    EstimatedEncodeNS int64
    EstimatedDecodeNS int64
    Reason            string
}

type Codec interface {
    ID() ID
    Name() string
    Estimate(src []byte, ctx Context) Estimate
    Encode(dst, src []byte, ctx Context) ([]byte, Metadata, error)
    Decode(dst, encoded []byte, meta Metadata, ctx Context) ([]byte, error)
}
```

The actual API may be refined, but it must preserve:

- deterministic encoding;
- explicit eligibility;
- estimated and actual size;
- bounded output;
- no hidden global state;
- decode metadata;
- clear error reporting.

### 11.2 Codec IDs

Reserve stable IDs from the beginning:

| ID | Codec |
|---:|---|
| 0 | Raw bytes |
| 1 | Canonical signed integer |
| 2 | Canonical unsigned integer |
| 3 | Canonical UUID |
| 4 | Canonical timestamp |
| 5 | JSON shape |
| 6 | Dictionary string |
| 7 | Delta-packed sequence |
| 8 | Bitmap integer set |
| 9 | LZ4 |
| 10 | Zstandard |
| 11-31 | Reserved for version 1 |

IDs must never be reused for an incompatible format.

### 11.3 Raw codec

The raw codec is the universal fallback. It must accept every value and add the smallest possible header.

Use raw storage when:

- no codec is eligible;
- estimated savings are below thresholds;
- the value is too small for compression metadata;
- the value changes too frequently;
- the optimizer has exhausted its CPU budget;
- encoding or verification fails;
- the operator disables optimization.

### 11.4 Canonical integer codecs

Only encode a decimal string as an integer when formatting is canonical and exactly reversible.

Allowed examples:

- `0`
- `17`
- `-42`

Raw fallback examples:

- `00`
- `+1`
- `-0`
- whitespace-padded numbers
- values outside the supported integer range

Use zigzag plus unsigned varint for signed values. `INCR` and related commands must detect overflow and match documented Redis-style error behavior.

### 11.5 UUID codec

Canonical UUID strings may be stored as 16 binary bytes plus minimal formatting flags. The first version should accept only one documented canonical form, preferably lowercase hexadecimal with hyphens. Other forms remain raw until exact reconstruction is implemented and tested.

### 11.6 Timestamp codec

Only canonical, fully reversible timestamp layouts are eligible. If a layout contains variable fractional precision, timezone spelling or formatting that the metadata cannot preserve, keep the value raw.

Do not infer timestamps from arbitrary numerical values.

### 11.7 General compression codecs

Use:

- LZ4 for warm values where fast decompression matters;
- Zstandard for colder and sufficiently large values where density matters more.

General compression must be skipped for:

- very small values;
- known already-compressed or encrypted signatures when detected cheaply;
- values whose sample indicates poor compressibility;
- write-heavy values;
- values whose compressed output plus metadata is not sufficiently smaller.

Dependencies and settings must be pinned and recorded. Decompression must enforce the stored raw length and configured maximum output to prevent memory amplification attacks.

---

## 12. Exact JSON shape encoding

JSON shape encoding is the central differentiator.

### 12.1 Requirement

For a value stored through ordinary `SET`, JSON encoding must reproduce the original bytes exactly. It must not normalize:

- whitespace;
- property ordering;
- Unicode escapes;
- number formatting;
- string escaping;
- duplicate object keys;
- trailing newline behavior.

### 12.2 Template model

Parse JSON into a token stream without discarding original byte spans. Separate the document into:

1. **template spans:** repeated structural and key bytes that can be shared;
2. **value slots:** variable literal byte sequences;
3. **slot type hints:** string, number, boolean, null, object or array boundary when useful.

Example input:

```json
{"country":"LV","status":"active","plan":"free","createdAt":"2026-09-07T10:30:00Z"}
```

Conceptual shared template:

```text
{"country":<S0>,"status":<S1>,"plan":<S2>,"createdAt":<S3>}
```

Conceptual record:

```text
schema=12, values=["LV", dict:active, dict:free, timestamp:1788777000]
```

The stored schema must retain every literal byte required to rebuild the exact input. Different whitespace or property ordering may intentionally produce a different schema.

### 12.3 Schema identity

A schema is identified by a content hash of its exact template and slot description. Hash matches must be confirmed by comparing schema content.

Schemas are local to a shard in version 1 to avoid global lock contention. Cross-shard schema sharing is a future optimization and requires measurement.

### 12.4 Schema admission

Do not create a permanent schema for every one-off JSON value. Use a bounded admission process:

1. Observe candidate shape hashes through a small frequency sketch.
2. Admit a schema only after a configurable repetition threshold.
3. Keep the original value raw until admission.
4. Allow the background optimizer to rewrite earlier matching values if beneficial.
5. Cap schema count and total schema memory per shard.
6. Evict schemas with zero references first, then low-value schemas.

### 12.5 Slot codecs

Each JSON value slot may use a nested, explicitly versioned compact representation:

- short inline bytes;
- canonical integer varint;
- UUID binary;
- timestamp binary;
- boolean/null tag;
- dictionary reference;
- raw literal span.

Nested compression should not recursively apply general compression to very small slots.

### 12.6 Verification

Codec tests must assert:

```text
decode(encode(input)) == input
```

Property and fuzz corpora must include:

- arbitrary valid JSON;
- invalid JSON, which must fall back safely;
- duplicate keys;
- deeply nested values up to the configured limit;
- unusual whitespace;
- escaped slashes and Unicode;
- large numbers that cannot fit native integer types;
- negative zero and exponent notation;
- empty arrays and objects;
- repeated and reordered fields;
- malformed UTF-8 inside otherwise arbitrary byte strings.

The parser must enforce depth, token-count and input-size limits.

---

## 13. Dictionary encoding

### 13.1 Purpose

Dictionary encoding replaces repeated values such as `active`, `free`, `Latvia` or repeated identifiers with compact integer references.

### 13.2 Scope

Use per-shard dictionaries initially. Each dictionary entry contains:

- stable local ID;
- original bytes;
- reference count;
- access/benefit estimate;
- generation;
- encoded and metadata cost.

### 13.3 Admission rule

A string should enter the dictionary only when expected total savings exceed:

- dictionary entry overhead;
- reference metadata;
- frequency-sketch overhead;
- likely lifetime and reclamation cost.

Do not intern every string. Unique values and very short values often become larger when dictionary metadata is included.

### 13.4 Reclamation

- Increment and decrement references atomically under the owning shard lock.
- Reclaim entries with zero references.
- Never reuse an ID while readers or persisted records may still reference the previous generation.
- Include dictionary memory in the hard memory budget.
- Verify reference accounting in tests after overwrite, delete, expiration, eviction and failed rewrite.

---

## 14. Adaptive policy engine

### 14.1 Policy inputs

The policy considers:

- raw value length;
- candidate encoded length including all metadata;
- measured or estimated encode/decode cost;
- recent read count;
- recent write count;
- time since last access;
- current codec;
- memory pressure;
- schema/dictionary availability;
- operator configuration;
- current optimizer CPU and scratch-memory budget.

### 14.2 Heat classes

Values may be classified as:

- **hot:** frequently read or latency-sensitive;
- **warm:** occasionally read and rarely modified;
- **cold:** infrequently accessed;
- **write-heavy:** modified often enough that recompression is undesirable;
- **unknown:** insufficient observations.

Heat is a hint, not client-visible state. Use a decaying or windowed score so old traffic does not keep a value permanently hot.

### 14.3 Initial decision rules

Exact thresholds must be configurable and benchmarked. Reasonable starting rules are:

- under 64 bytes: try only cheap typed/packed codecs;
- 64-255 bytes: require at least 32 bytes and 15% net saving;
- 256 bytes and above: consider structural and general compression;
- LZ4: require at least 12.5% net saving after metadata;
- Zstandard: require at least 20% net saving after metadata;
- never run expensive compression synchronously for a hot write path;
- prefer JSON shape encoding when a schema is already admitted and decoding is cheaper than general compression;
- keep write-heavy values raw or cheaply packed;
- under memory pressure, increase density preference within the configured CPU budget.

These are starting hypotheses, not product claims.

### 14.4 Scoring

Candidate selection should use an explainable score such as:

```text
netSavedBytes = currentPhysicalBytes - candidatePhysicalBytes

score = netSavedBytes
        - encodeCostWeight * estimatedEncodeNanoseconds
        - decodeCostWeight * estimatedDecodeNanoseconds * expectedReads
        - rewriteRiskPenalty
```

The policy must first enforce minimum saving thresholds. The score then chooses among eligible candidates.

### 14.5 Explainability

`SNUG.POLICY key` should report information similar to:

```text
current_codec: raw
heat_class: warm
raw_bytes: 742
candidate: json-shape
candidate_bytes: 231
net_saved_bytes: 511
decision: selected
reason: admitted schema 17; saving 68.8%; low write frequency
```

Do not expose sensitive value contents in diagnostics.

---

## 15. Background optimizer

### 15.1 Responsibilities

The optimizer:

- samples entries without scanning the entire database continuously;
- updates heat classes;
- evaluates better encodings;
- admits useful JSON schemas and dictionary entries;
- rewrites eligible values;
- releases unused schemas and dictionaries;
- compacts fragmented arena segments when profitable;
- respects CPU, memory and latency budgets.

### 15.2 Safe rewrite protocol

Use an optimistic version-check workflow:

1. Under the shard read lock, capture key identity, value reference, codec, raw length and entry version.
2. Copy or safely pin the immutable encoded bytes.
3. Release the lock.
4. Decode and evaluate candidates outside the lock.
5. Produce a proposed immutable encoded record.
6. Acquire the shard write lock.
7. Verify that the key still exists and its version and value identity are unchanged.
8. If unchanged, publish the new record, update references/accounting and retire the old allocation.
9. If changed, discard the candidate without modifying the current entry.

The rewrite itself is an internal representation change and must not alter TTL or logical value. Whether it increments the public mutation version should be decided in an ADR; the internal CAS version must still prevent stale commits.

### 15.3 Budgets

Configure:

- maximum optimizer CPU percentage;
- worker count;
- maximum bytes encoded per second;
- maximum scratch bytes;
- maximum queue depth;
- minimum time between rewrites of the same key;
- pause threshold based on request latency;
- pause behavior during persistence snapshots.

### 15.4 Anti-thrashing

Use hysteresis. A value should not continuously alternate between LZ4 and Zstandard because of small heat fluctuations. Require a meaningful score improvement and a minimum residency time before re-encoding.

---

## 16. Concurrency model

### 16.1 Version 1 model

- one `RWMutex` per shard;
- reads take a read lock while resolving the entry and obtaining safe bytes;
- mutations take the shard write lock;
- encoding that may be expensive occurs outside the write lock when possible;
- publication and accounting occur atomically under the write lock;
- multi-key commands group keys by shard and acquire locks in ascending shard order;
- never acquire shard locks in an inconsistent order;
- avoid holding locks while writing network responses.

### 16.2 Atomic operations

`INCR`, `DECR`, expiration changes, conditional `SET`, overwrite and deletion must be atomic per key.

`MSET` atomicity across shards is not guaranteed in version 1 unless explicitly implemented and documented. Match the published compatibility contract; do not imply stronger semantics.

### 16.3 Reader safety

The version 1 implementation may decode under the shard read lock for simplicity. If this creates unacceptable contention, introduce immutable segment generations or reference pinning through an ADR and dedicated stress tests.

Do not return arena-backed slices to callers after releasing the protection that keeps them alive.

### 16.4 Race testing

Concurrency tests must cover:

- reads racing with overwrite;
- reads racing with expiration;
- optimizer racing with overwrite;
- optimizer racing with delete and recreate;
- dictionary references racing with eviction;
- snapshot creation during writes;
- simultaneous `INCR` operations;
- multi-key operations crossing shards;
- shutdown while clients and optimizer workers are active.

---

## 17. TTL and expiration

Use two layers:

1. **lazy expiration:** every key lookup checks `ExpiresAtMS` and treats an expired key as missing;
2. **active expiration:** a background process reclaims expired entries without requiring reads.

The initial active expiration implementation may use per-shard min-heaps or bounded sampling. A timing wheel may be added when measurement justifies it.

Requirements:

- use a monotonic-safe time abstraction where applicable;
- centralize clock access behind an interface for deterministic tests;
- deleting an expired key must update schemas, dictionaries and memory accounting;
- TTL must survive an internal re-encoding;
- persistence recovery must restore absolute expiration correctly;
- `TTL` and `PTTL` semantics must be documented and tested at boundaries.

---

## 18. Eviction and memory pressure

### 18.1 Policies

Support initially:

- `noeviction`;
- `allkeys-lru` or an approximate sampled LRU;
- `volatile-lru` after TTL metadata is stable.

Additional policies may follow later.

### 18.2 Order of response to memory pressure

When nearing the soft limit:

1. prioritize cheap beneficial re-encoding;
2. reclaim expired keys;
3. reclaim zero-reference schemas and dictionary entries;
4. compact highly fragmented segments when budget permits;
5. evict according to policy if still necessary.

Do not perform unbounded synchronous compaction in a client request.

### 18.3 Hard-limit behavior

Before acknowledging a write, estimate its worst-case accounted allocation. If the engine cannot satisfy the request under the configured policy and bounded temporary budget, return an out-of-memory error without partially applying the command.

The system must not enter a retry loop that repeatedly compresses and expands the same values under pressure.

---

## 19. Network protocol and server

### 19.1 RESP parser

Implement RESP2 incrementally with a streaming parser. Enforce:

- maximum request bytes;
- maximum bulk-string bytes;
- maximum array elements;
- maximum nesting where relevant;
- read and write deadlines;
- maximum queued output per connection;
- clean handling of partial TCP frames;
- clean handling of pipelined requests;
- no allocation proportional to an untrusted declared size before validation.

### 19.2 Connection handling

One goroutine per connection is acceptable initially. Command work should route to the relevant shard without creating a goroutine per request.

Use bounded buffers and pools carefully. A pooled buffer must not retain arbitrarily large allocations forever; discard buffers above a configurable threshold.

### 19.3 Backpressure

Slow clients must not consume unlimited memory. Close or throttle connections whose pending response bytes exceed configured bounds.

### 19.4 Error responses

Return clear RESP errors for:

- unknown commands;
- incorrect arity;
- invalid integer or expiration arguments;
- overflow;
- unsupported database selection;
- out-of-memory conditions;
- malformed protocol input;
- values exceeding limits;
- persistence failures when the configured policy requires write rejection.

---

## 20. Persistence

Persistence begins only after the in-memory MVP is correct and benchmarked.

### 20.1 Append-only file

The AOF records logical mutations, not internal rewrite operations. Re-encoding a value must not appear as a client mutation.

Initial fsync policies:

- `always`;
- `everysec`;
- `no`.

Requirements:

- checksummed or safely framed records;
- detection of truncated final records;
- documented recovery behavior;
- bounded write buffering;
- clear response policy if durable append fails;
- AOF rewrite to remove overwritten/deleted history;
- atomic replacement of rewritten AOF.

### 20.2 Snapshots

A snapshot contains:

- format version;
- configuration required for decoding;
- schema and dictionary records;
- keys, encoded values and TTLs;
- checksums;
- creation timestamp;
- codec registry compatibility information.

Snapshots may store internal encoded values to preserve startup speed, but every format must be versioned and verified during load.

### 20.3 Recovery

Recovery order:

1. load the latest valid snapshot if configured;
2. rebuild schema/dictionary references;
3. replay subsequent AOF mutations;
4. discard already expired keys;
5. validate memory accounting;
6. begin accepting connections only after the configured readiness condition is met.

Corruption must produce a clear failure or documented salvage mode. Never silently invent missing data.

---

## 21. Internal value format

### 21.1 Versioned envelope

The persisted or portable encoded envelope should contain conceptually:

```text
magic | format-version | codec-id | flags | raw-length | payload-length |
codec-metadata | payload | checksum
```

Use varints for lengths where this saves space without making parsing unsafe. Define endianness for all fixed-width fields. The detailed format belongs in `docs/memory-format.md` and requires golden test vectors.

### 21.2 Checksums

In-memory records do not necessarily require a checksum. Persisted records do. Optional debug builds may verify in-memory records to detect codec bugs.

### 21.3 Forward compatibility

Unknown codec IDs or newer incompatible format versions must fail clearly during recovery. A migration utility may decode an older known format and write the current one.

---

## 22. Observability

### 22.1 Required metrics

Expose at least:

- commands by name and result;
- request duration histogram by command;
- active connections;
- input/output bytes;
- key count;
- expired and evicted key counts;
- physical allocated bytes;
- live dataset bytes;
- metadata/overhead bytes;
- fragmentation bytes and ratio;
- original logical value bytes;
- encoded value bytes;
- bytes saved by codec;
- entries by codec;
- encode/decode duration by codec;
- codec attempt, success, rejection and error counts;
- optimizer queue depth and throughput;
- optimizer CPU/scratch budget usage;
- stale rewrite rejection count;
- schema/dictionary count and bytes;
- AOF/snapshot state when enabled.

### 22.2 Logs

Use structured logs. Do not log keys or values by default. Include safe identifiers such as shard number, codec name, error category and duration.

Rate-limit repetitive codec or persistence errors.

### 22.3 Administrative output

`INFO memory` should separate:

- `used_memory_physical`;
- `used_memory_dataset`;
- `used_memory_overhead`;
- `used_memory_fragmentation`;
- `logical_value_bytes`;
- `encoded_value_bytes`;
- `estimated_bytes_saved`.

Document that process RSS may differ because of allocator behavior, stacks, mapped files and operating-system accounting.

---

## 23. Configuration

Support a configuration file plus command-line and environment overrides. The exact syntax may be YAML, TOML or a Redis-like text format; choose one and record it in an ADR.

Illustrative configuration:

```yaml
server:
  listen: "127.0.0.1:6380"
  max_connections: 10000
  read_timeout: 30s
  write_timeout: 30s
  max_request_bytes: 64MiB
  max_pipeline_commands: 1024

engine:
  shards: 256
  max_memory: 4GiB
  eviction_policy: noeviction
  arena_segment_size: 4MiB

optimization:
  enabled: true
  workers: 2
  max_cpu_percent: 10
  max_bytes_per_second: 64MiB
  max_scratch_memory: 128MiB
  min_rewrite_interval: 5m
  schema_admission_count: 8
  schema_memory_limit: 64MiB
  dictionary_memory_limit: 64MiB
  lz4_enabled: true
  zstd_enabled: true
  json_shape_enabled: true

persistence:
  mode: none
  aof_path: "data/snugkv.aof"
  fsync: everysec

observability:
  metrics_listen: "127.0.0.1:9090"
  log_level: info
```

Requirements:

- validate the entire configuration before opening a listening socket;
- reject shard counts that are not powers of two;
- print safe effective configuration without secrets;
- document which settings require restart;
- keep runtime policy changes out of version 1 unless required.

---

## 24. Security requirements

1. Bind to loopback by default.
2. Treat all protocol input as untrusted.
3. Enforce request, depth, output and decompression limits.
4. Prevent decompression bombs by validating declared raw size before allocation.
5. Avoid logging secrets, keys or values.
6. Provide optional password/token authentication only after the protocol core is stable.
7. Prefer TLS termination through a trusted proxy initially; native TLS may be added later.
8. Do not implement dynamic code execution or scripting in version 1.
9. Use constant-time comparison for authentication secrets.
10. Ensure metrics and administrative interfaces bind separately and securely.
11. Run fuzzing against RESP and structured codecs.
12. Include malformed snapshot/AOF data in fuzz and recovery tests.

---

## 25. Testing strategy

### 25.1 Unit tests

Cover:

- RESP parsing and serialization;
- every command's success and failure semantics;
- key hashing and collisions;
- index insertion/deletion/resize;
- arena allocation/free/accounting;
- every codec's eligibility boundaries;
- exact round-trip;
- policy decisions;
- expiration boundaries;
- eviction;
- persistence framing and recovery;
- configuration validation.

### 25.2 Property tests

Required properties:

1. `decode(encode(v)) == v` for every codec-eligible `v`.
2. A failed codec attempt leaves the original value unchanged.
3. Internal re-encoding does not change GET output or TTL.
4. Memory counters never become negative.
5. Schema and dictionary reference counts equal live references after a full audit.
6. Index operations return the correct key under forced hash collisions.
7. Applying the same logical state through different internal encodings produces identical client behavior.

### 25.3 Fuzz tests

Fuzz:

- arbitrary TCP/RESP byte streams;
- integer parsing and overflow;
- JSON tokenizer and shape codec;
- compressed envelope decoding;
- snapshot/AOF loaders;
- command option parsing;
- malformed UUID/timestamp candidates;
- deeply nested and adversarial inputs.

Fuzz functions must include corpus seeds for previously fixed bugs.

### 25.4 Concurrency and stress tests

Run randomized concurrent operations against both SnugKV and a simple reference model. Include overwrites, deletes, expirations, counters and forced background rewrites.

Test under `go test -race` and with repeated execution.

### 25.5 Compatibility tests

Use at least:

- `redis-cli` for manual and scripted checks;
- one Go Redis client;
- one Node.js Redis client relevant to the expected user base.

Compare supported command semantics with Redis using an explicit compatibility test suite. Differences must be documented.

### 25.6 Fault-injection tests

Inject:

- short writes;
- disk full;
- fsync failure;
- corrupt final AOF record;
- corrupt snapshot block;
- optimizer cancellation;
- shutdown during rewrite;
- allocation budget exhaustion;
- slow clients;
- connection reset during pipelining.

---

## 26. Benchmark plan

### 26.1 Principles

- publish hardware, Go version, OS and configuration;
- separate steady-state from load-time measurements;
- warm up before recording latency;
- report multiple runs and variance;
- report throughput and p50/p95/p99 latency;
- record process RSS and engine-accounted memory;
- verify returned data while benchmarking;
- compare against a raw SnugKV mode to isolate encoding benefit;
- compare with Redis only using equivalent supported semantics;
- do not tune only for one favorable dataset.

### 26.2 Required datasets

#### A. Repeated session JSON

At least one million records with:

- stable property names and order;
- UUIDs;
- timestamps;
- booleans;
- small enums;
- variable user identifiers;
- realistic value-length distribution.

#### B. API responses

Objects with several recurring shapes, nested arrays and moderate text.

#### C. Industrial telemetry

Equipment IDs, timestamps, status enums and numerical sequences with both stable and changing values.

#### D. Random/incompressible values

Random bytes across size buckets. This validates that the policy avoids harmful compression.

#### E. Hot counters

Small integer values with high mutation rates.

#### F. Already-compressed data

JPEG, PNG, gzip and encrypted-looking samples to verify fast rejection.

#### G. Mixed temperature workload

- 10% hot keys receiving 90% of reads;
- a large cold population;
- periodic writes;
- TTL churn.

### 26.3 Measurements

For each dataset record:

- logical input bytes;
- key bytes;
- physical engine allocation;
- RSS;
- bytes per key;
- encoding distribution;
- schemas and dictionary memory;
- compression ratio by codec;
- load throughput;
- GET and SET throughput;
- p50/p95/p99 latency;
- optimizer CPU usage;
- optimizer convergence time;
- temporary peak memory during optimization;
- fragmentation before and after compaction.

### 26.4 Initial success targets

These are engineering targets, not promises:

1. At least 40% lower accounted memory than raw mode on repeated session JSON after optimizer convergence.
2. At least 25% lower accounted memory on the API-response dataset.
3. No more than 8% accounted-memory overhead versus raw mode for incompressible values.
4. Hot GET p99 no worse than 1.5 times raw mode for packed structural codecs.
5. No unbounded allocation growth during repeated encode/decode cycles.
6. Optimizer remains within configured CPU and scratch-memory budgets.
7. Zero data mismatches across the complete benchmark.

The aspirational product hypothesis is two to five times more application records per gigabyte for highly repetitive structured workloads. It must remain labeled as a hypothesis until independent benchmarks demonstrate it.

---

## 27. Milestone plan

### Milestone 0: repository and measurement foundation

Deliver:

- Go module and directory structure;
- CI running formatting, vet, tests and race tests;
- configuration loader;
- benchmark harness;
- memory-accounting interfaces;
- `PROGRESS.md` and initial ADR template;
- Dockerfile and local compose configuration.

Acceptance:

- clean checkout builds and tests with one documented command;
- benchmark output is machine-readable and includes environment metadata.

### Milestone 1: correct raw key-value server

Deliver:

- RESP2 streaming parser;
- TCP server;
- sharded engine using raw values;
- core `PING`, `SET`, `GET`, `DEL`, `EXISTS`, `MGET` commands;
- exact byte round-trip;
- bounded request and connection buffers;
- graceful shutdown;
- basic metrics.

Acceptance:

- supported commands work through `redis-cli`, Go and Node clients;
- fuzz parser passes without panic;
- race detector passes;
- one million mixed keys load successfully within configured memory.

### Milestone 2: TTL, counters and hard memory accounting

Deliver:

- expiration commands;
- lazy and active expiration;
- canonical integer codec;
- atomic counters;
- physical/logical memory accounting;
- hard limit and `noeviction` behavior;
- `INFO memory` and `SNUG.MEMORY`.

Acceptance:

- expiration boundary tests use a fake clock;
- concurrent counters are correct;
- failed out-of-memory writes do not partially mutate state;
- accounting audits match live allocations.

### Milestone 3: codec framework and cheap packed codecs

Deliver:

- codec registry and stable IDs;
- raw, integer, UUID and canonical timestamp codecs;
- codec metadata envelope;
- selection thresholds;
- `SNUG.ENCODING` and `SNUG.POLICY`;
- property and fuzz tests.

Acceptance:

- all accepted values round-trip exactly;
- noncanonical values remain raw;
- memory results include metadata cost;
- codec errors fall back safely.

### Milestone 4: JSON shape and dictionary encoding

Deliver:

- byte-preserving JSON tokenizer;
- template/slot extraction;
- schema frequency sketch and bounded admission;
- per-shard schema store;
- bounded dictionary encoding;
- nested slot codecs;
- reference reclamation;
- session/API benchmark datasets.

Acceptance:

- JSON fuzzing produces no mismatches or panics;
- arbitrary formatting and duplicate keys round-trip exactly;
- schemas are not leaked under write/delete/expiration churn;
- session dataset meets or approaches the memory target with transparent GET output.

### Milestone 5: adaptive optimizer and general compression

Deliver:

- heat tracking;
- bounded optimizer queue/workers;
- safe version-checked rewrites;
- LZ4 and Zstandard codecs;
- hot/warm/cold policies;
- anti-thrashing hysteresis;
- `SNUG.COMPACT`;
- optimizer metrics.

Acceptance:

- stale rewrites are rejected in deterministic race tests;
- optimizer obeys CPU, byte-rate and scratch budgets;
- frequently modified values do not continuously recompress;
- mixed-temperature benchmarks report convergence and latency.

### Milestone 6: packed index and arena compaction

Deliver:

- open-addressed index behind existing interface;
- segmented key/value arenas;
- free lists or size classes;
- fragmentation tracking;
- safe background compaction;
- comparison with initial Go-map index.

Acceptance:

- forced collision tests pass;
- no reader observes reused memory;
- compaction does not change values or TTLs;
- memory reduction is separately attributable and benchmarked.

### Milestone 7: eviction

Deliver:

- sampled access tracking;
- `allkeys-lru`;
- optional `volatile-lru`;
- pressure-response order;
- eviction metrics and tests.

Acceptance:

- memory remains bounded under sustained writes;
- eviction policy behaves predictably in seeded tests;
- schema/dictionary references remain valid after eviction.

### Milestone 8: persistence

Deliver:

- versioned AOF;
- configured fsync modes;
- AOF recovery and rewrite;
- snapshots;
- checksums and corruption handling;
- recovery metrics and operational documentation.

Acceptance:

- restart reproduces logical state and TTL behavior;
- truncated final AOF record follows documented recovery behavior;
- internal re-encodings do not create logical AOF mutations;
- fault-injection suite passes.

### Milestone 9: release candidate

Deliver:

- complete supported-command documentation;
- security review;
- benchmark report;
- operational runbook;
- Docker image;
- upgrade/recovery documentation;
- reproducible demo workload;
- known limitations.

Acceptance:

- all global acceptance criteria pass;
- 24-hour mixed workload soak test shows no corruption, race or unbounded growth;
- public claims match benchmark evidence.

---

## 28. Global acceptance criteria

The version 1 release is complete only when all of the following are true:

### Correctness

- every supported value round-trips byte-for-byte;
- supported command semantics are documented and tested;
- concurrent mutation and optimization cannot publish stale data;
- TTL and eviction cannot leave invalid schema/dictionary references;
- race detector and fuzz suites pass;
- recovery is deterministic when persistence is enabled.

### Memory

- all major allocations appear in memory reporting;
- structured benchmark memory is materially lower than raw mode;
- incompressible data is not repeatedly recompressed;
- optimizer scratch memory is bounded;
- schema/dictionary growth is bounded;
- fragmentation can be measured and reclaimed.

### Performance

- request latency is reported by command and codec;
- hot values avoid dense high-cost codecs;
- optimizer work does not create sustained request-latency spikes above documented targets;
- slow clients cannot produce unbounded output queues.

### Operability

- graceful shutdown works;
- configuration errors fail before serving traffic;
- metrics contain no value contents;
- codec and optimizer decisions are inspectable;
- persistence failures are visible and follow configured write policy;
- a new operator can run the server and benchmark from the README.

---

## 29. Key design risks and mitigations

### Risk: metadata eliminates savings

**Mitigation:** calculate actual physical bytes, including schema and dictionary amortization, before selecting a codec.

### Risk: JSON encoding changes client-visible bytes

**Mitigation:** token-span templates preserve exact bytes; property and fuzz tests enforce equality.

### Risk: compression increases latency

**Mitigation:** heat-aware codec policy, minimum-saving thresholds, background work and operator budgets.

### Risk: background optimizer overwrites newer data

**Mitigation:** immutable candidate records and entry-version comparison at commit.

### Risk: schema or dictionary memory grows forever

**Mitigation:** bounded admission, reference counts, zero-reference reclamation and strict memory budgets.

### Risk: representation oscillates

**Mitigation:** hysteresis, minimum residency time and meaningful-improvement thresholds.

### Risk: Go heap overhead hides payload savings

**Mitigation:** packed metadata, byte arenas, offset references and measured RSS/accounting comparisons.

### Risk: large values cause temporary memory spikes

**Mitigation:** scratch allocator budgets, maximum decoded length, streaming where practical and rejection before allocation.

### Risk: excessive Redis compatibility expands scope

**Mitigation:** publish a strict supported-command list and return explicit errors elsewhere.

### Risk: benchmarks favor only one dataset

**Mitigation:** require structured, mixed, random, compressed, counter and telemetry datasets.

---

## 30. Decisions that require an ADR

Create an ADR before changing or selecting:

- configuration format;
- hash algorithm;
- initial and packed index designs;
- arena segment sizes and lifetime model;
- schema admission sketch;
- compression libraries and settings;
- heat-scoring formula;
- whether internal rewrites increment entry version;
- cross-shard multi-key atomicity;
- persistence record format;
- metrics library;
- authentication strategy;
- unsafe or memory-mapped storage;
- any departure from exact byte round-trip;
- any new external dependency with material runtime impact.

ADR template:

```markdown
# ADR-NNN: Decision title

## Status
Proposed | Accepted | Superseded

## Context
What problem or constraint requires a decision?

## Options considered
What realistic choices were evaluated?

## Decision
What was selected?

## Consequences
What improves, what becomes harder and what must be measured?

## Validation
Which tests or benchmarks prove the decision is acceptable?
```

---

## 31. Agent handoff protocol

At the end of every agent session, update `PROGRESS.md` with:

```markdown
# Progress

## Current milestone
Milestone N — name

## Completed
- Item and relevant tests

## In progress
- Item, current state and affected files

## Verification performed
- Exact commands and results

## Known issues
- Reproducible issue, severity and workaround

## Decisions made
- ADR links or small non-architectural decisions

## Next recommended task
- One bounded next step with acceptance condition
```

An agent must not mark a milestone complete while required acceptance tests are missing or failing.

When taking over existing work, an agent must:

1. read this specification;
2. read `PROGRESS.md`;
3. inspect recent ADRs;
4. run the existing test suite;
5. reproduce any recorded failure before attempting a fix;
6. continue the smallest unfinished task in the current milestone.

---

## 32. Suggested first agent tasks

The first implementation agent should execute these tasks in sequence:

1. Initialize the Go module and repository layout.
2. Create `PROGRESS.md` and the ADR template.
3. Implement strict configuration parsing with defaults and validation.
4. Implement the RESP2 parser with table tests and a fuzz target.
5. Implement an in-memory raw sharded engine behind interfaces.
6. Connect `PING`, `SET`, `GET`, `DEL` and `EXISTS` through the TCP server.
7. Add graceful shutdown and connection limits.
8. Add exact-byte integration tests using TCP rather than only package calls.
9. Add a raw-mode memory and throughput benchmark.
10. Stop and record baseline results before implementing compression.

This sequence creates a trustworthy baseline. Compression work without a correct, measured raw baseline is not acceptable.

---

## 33. Future roadmap

Only consider these after version 1 acceptance:

- RESP3;
- hashes, lists, sets and sorted sets;
- semantic typed JSON commands;
- Zstandard trained dictionaries;
- cross-shard schema sharing;
- tenant-aware dictionaries and limits;
- embedded Go API;
- replication;
- Raft-based high availability;
- partitioning and cluster routing;
- disk/NVMe cold tier;
- S3 snapshot backup;
- Kubernetes operator;
- adaptive policies learned from historical telemetry;
- SIMD-assisted codecs where portable and justified;
- eBPF or low-level profiling integrations;
- compatibility proxy mode in front of Redis;
- online migration from Redis using scan and dual writes.

---

## 34. Glossary

**AOF:** Append-only file containing logical mutations for recovery.  
**Arena:** A large byte allocation subdivided into smaller records, reducing per-object overhead.  
**Codec:** A reversible transformation between original bytes and an internal representation.  
**Cold value:** A value accessed infrequently enough to favor density over minimum decode latency.  
**Dictionary encoding:** Replacing repeated byte sequences with compact identifiers.  
**Exact round-trip:** Decoding produces the identical byte sequence originally supplied.  
**Fragmentation:** Allocated memory that cannot currently hold live data efficiently.  
**Heat:** A decaying measure of how frequently a value is read or written.  
**Hysteresis:** Requiring a meaningful threshold before changing state, preventing repeated oscillation.  
**Idempotent internal operation:** Repeating an internal action produces no additional logical change.  
**JSON shape:** The exact repeated structural template shared by similar JSON documents.  
**Packed representation:** A compact typed binary form without general compression.  
**RESP:** Redis Serialization Protocol.  
**Schema admission:** The policy deciding when a repeated JSON shape deserves shared storage.  
**Scratch budget:** Maximum temporary memory available to encoding and decoding.  
**Warm value:** A value between hot and cold, suitable for fast lightweight compression.  

---

## 35. Final implementation principle

SnugKV should not compress data merely because it can. It should transform data only when the complete system cost improves.

The governing principle is:

> Use the smallest reversible representation whose memory saving is worth its latency, CPU, metadata and operational complexity for the observed workload.

Every architectural decision, codec and benchmark should be evaluated against that principle.
