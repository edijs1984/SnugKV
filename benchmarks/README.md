# Benchmark results

## Redis 8.2 comparison — 1M repetitive scalar keys, 2026-09-20

Black-box RESP2/TCP measurements on Linux amd64, Go 1.27.1, 4 logical CPUs,
1,000,000 keys, 256-byte deliberately repetitive values, 4 GET workers, and GET
pipeline depth 256. Redis and SnugKV were preloaded with the same logical
dataset. These are development measurements; repeated clean runs are required
before making public performance claims.

### Memory after load

| Server/config | Reported memory | Bytes/key |
|---|---:|---:|
| Redis 8.2 | 393,260,912 B | 392.39 B/key load delta |
| SnugKV raw | 362,314,176 B | 362.27 B/key load delta |
| SnugKV optimized | ~162,638,528 B settled | ~162.6 B/key accounted |

For this synthetic highly-compressible workload, optimized SnugKV used about
58.6% less reported/accounted memory than Redis while preserving all 1,000,000
logical 256-byte values. This percentage is workload-specific and must not be
generalized to incompressible data.

### Pipelined GET

| Server/config | GET ops/s | p50 | p95 | p99 |
|---|---:|---:|---:|---:|
| Redis 8.2 clean reference | 429,664 | 9.38 us | 12.70 us | 18.61 us |
| SnugKV raw reference | 526,951 | 6.93 us | 11.73 us | 17.41 us |
| SnugKV optimized, before response-copy fix | 404,451 | 8.43 us | 18.07 us | 27.83 us |
| SnugKV optimized, after response-copy fix | 430,233 | 8.05 us | 15.69 us | 25.28 us |
| SnugKV optimized, reusable decode scratch | 455,538 | 7.79 us | 13.49 us | 20.29 us |
| SnugKV optimized, GET dispatch bypass | 506,322 | 7.21 us | 12.34 us | 19.15 us |
| SnugKV optimized, redundant activity write removed | 522,399 | 7.00 us | 11.98 us | 18.61 us |
| SnugKV optimized, byte-key GET + buffered RESP length parsing | 547,522 avg / 553,991 best | 6.69 us best | 10.82 us best | 15.43 us best |
| SnugKV optimized, direct hot-codec DecodeInto dispatch | 554,543 avg / 564,404 best | 6.54 us best | 10.69 us best | 14.82 us best |
| SnugKV optimized, reusable buffered GET decode | 599,559 avg / 612,393 best | 5.95 us best | 9.88 us best | 14.30 us best |

The GET percentile samples for pipelined runs are amortized per-operation batch
times, not independent request latencies. The later optimized path combines
caller-owned decode scratch, direct bulk framing, a known-GET dispatch path after
authorization, and pointer-only activity metadata updates.

Across three consecutive clean SnugKV runs after byte-key lookup and buffered RESP
length parsing, throughput averaged 547,522 GET/s (best 553,991). After direct
hot-codec DecodeInto dispatch, three consecutive runs averaged 554,543 GET/s,
with a best observed run of 564,404 GET/s. Reusing a per-connection key scratch
for complete already-buffered GET frames raised the next three-run average to
599,559 GET/s, with a best observed run of 612,393 GET/s. Three Redis 8.2
runs in the same comparison window averaged 435,561 GET/s. On this exact
synthetic workload, SnugKV averaged about 25.7% higher GET throughput while
retaining the optimized memory footprint. This is not a universal Redis performance claim:
machine load materially affected earlier runs, and value compressibility strongly
affects the optimized path.

Reproduce with `cmd/rediswirebench`.

## Redis 8.2 comparison — random 256-byte SET/load, 2026-09-20

Black-box RESP2/TCP measurements on the same Linux amd64 / Go 1.27.1 development
machine with 4 logical CPUs, pipeline depth 256, 8 concurrent load workers, and
256-byte pseudo-random values. The load harness uses unique key ranges per worker.
Percentile samples are amortized per-operation batch times, not independent request
latencies.

For 1,000,000 keys:

| Server/config | SET/load throughput | Reported/accounted delta | Bytes/key |
|---|---:|---:|---:|
| Redis 8.2 reference | ~318,145 ops/s | ~392,388,584 B | ~392.39 |
| SnugKV optimized, five-run median | 315,256 ops/s | 362,268,608 B | 362.27 |
| SnugKV optimized, best of five | 318,331 ops/s | 362,268,608 B | 362.27 |

The five SnugKV runs were 287,303; 265,795; 315,256; 318,331; and 316,328
ops/s. On this exact workload the median was about 0.9% below the recorded Redis
8-worker reference while SnugKV's engine-accounted load delta was about 7.7%
lower per key. This is a development comparison, not a universal throughput or
RSS claim.

For sustained 5,000,000-key SnugKV loads on the same configuration, three
consecutive runs measured 299,270; 303,496; and 283,798 ops/s, for a median of
299,270 ops/s and a mean of about 295,521 ops/s. The measured load delta was
1,763,407,552 bytes, or 352.68 bytes/key. No Redis 5M result is recorded here,
so this figure is not presented as a Redis comparison.

The optimized SET path reached this point through allocation and hot-path work
rather than disabling memory features: plain SET borrows transient request bytes
until arena ownership, complete pipelined SET frames use reusable connection
scratch, optimizer candidates reuse per-worker scratch and borrowed raw fallbacks,
known key hashes are carried into indexed publication, and background optimization
yields more aggressively under sustained queue pressure. In profiling of the
random workload, temporary allocation traffic fell from roughly 1.38 GB to about
467 MB for the 1M load profile; the remaining major allocations were predominantly
persistent arena/index/entry growth.

Reproduce with `cmd/rediswirebench`.

This page records engineering measurements used to guide SnugKV storage decisions.
They are workload-specific measurements, not universal performance claims.

## Scalar codec benchmark — 2026-09-07

Single-run Linux amd64 measurements on Go 1.27.1, Intel Core i3-7020U
(4 logical CPUs), GOMAXPROCS=4, 256 shards, seed 1, and 10,000 records. Every read
was checked against its input. Accounted bytes use the engine model; process heap
includes the harness.

| Dataset/config | Logical value bytes | Encoded payload | Accounted bytes | GET p50/p95/p99 ns |
|---|---:|---:|---:|---:|
| sessions raw | 5,598,890 | 5,598,890 | 14,362,858 | 2,126 / 6,557 / 12,470 |
| sessions `-encoding -json-shape` | 5,598,890 | 2,068,890 | 8,169,898 | 4,989 / 7,266 / 15,155 |
| API raw | 4,797,780 | 4,797,780 | 9,365,866 | 2,022 / 10,203 / 14,918 |
| API `-encoding -json-shape` | 4,797,780 | 1,083,484 | 6,081,002 | 2,713 / 4,842 / 9,328 |
| random 256-byte `-encoding -compression` | 2,560,000 | 2,560,000 | 9,365,866 | 1,710 / 2,485 / 3,806 |
| gzip-wrapped random 256-byte `-encoding -compression` | 2,810,000 | 2,810,000 | 9,365,866 | 1,440 / 2,893 / 5,764 |
| compressible 4 KiB raw (5,000 keys) | 20,480,000 | 20,480,000 | 42,946,762 | 4,390 / 7,345 / 13,238 |
| compressible 4 KiB `-encoding -compression` (5,000 keys) | 20,480,000 | 310,000 | 3,845,322 | 4,111 / 6,548 / 11,639 |

On these runs, accounted memory was 43.1% lower for session JSON and 35.1% lower
for API JSON after template optimization and compaction. Random and already-
compressed values stayed raw. The deliberately repetitive 4 KiB workload selected
LZ4 and reduced accounted memory by 91.0%; it is synthetic and not representative
of arbitrary data.

Reproduce scalar runs with `cmd/snugbench`.

## Native container benchmarks — 2026-09-15

The native datatype runs use 100,000 keys, 256 shards, and compare SnugKV
engine-accounted memory deltas with Redis `used_memory` deltas. These are not RSS
comparisons. Redis DB 15 was used as the disposable benchmark database. Container
payloads are binary-safe; the SET/LIST/ZSET matrices below use 16-byte members or
elements.

### HASH

The HASH benchmark varies field count and schema reuse. Shared-schema results:

| Fields/hash | Snug B/hash | Redis B/hash | Snug memory saving |
|---:|---:|---:|---:|
| 4 | 254.65 | 258.33 | 1.43% |
| 8 | 390.96 | 450.33 | 13.18% |
| 16 | 644.26 | 834.33 | 22.78% |
| 32 | 1252.66 | 1602.33 | 21.82% |
| 64 | 2445.88 | 3138.33 | 22.06% |

Mixed 80/20 shared/unique schemas:

| Fields/hash | Snug B/hash | Redis B/hash | Snug memory saving |
|---:|---:|---:|---:|
| 4 | 265.63 | 258.33 | -2.82% |
| 8 | 405.87 | 450.33 | 9.87% |
| 16 | 694.72 | 834.33 | 16.73% |
| 32 | 1424.43 | 1602.33 | 11.10% |
| 64 | 2881.40 | 3138.33 | 8.19% |

Fully unique schemas are weaker at low field counts: -14.49% at 4 fields and
-4.42% at 8, approximately tied at 16, then +2.60% and +2.39% at 32/64. This is
why HASH storage is frozen rather than further special-cased: shared layouts win
strongly and unique layouts remain close enough for v1.

Run with `cmd/hashbench`.

### SET

Sequential/structured members:

| Members/set | Snug B/set | Redis B/set | Snug memory saving |
|---:|---:|---:|---:|
| 1 | 127.73 | 91.78 | -39.16% |
| 4 | 148.87 | 146.33 | -1.73% |
| 8 | 190.64 | 226.33 | 15.77% |
| 16 | 254.39 | 386.33 | 34.15% |
| 32 | 389.71 | 706.33 | 44.83% |
| 64 | 644.32 | 1346.33 | 52.14% |

Dispersed members:

| Members/set | Snug B/set | Redis B/set | Snug memory saving |
|---:|---:|---:|---:|
| 1 | 127.73 | 98.34 | -29.89% |
| 4 | 170.17 | 146.33 | -16.29% |
| 8 | 254.39 | 226.33 | -12.40% |
| 16 | 389.71 | 386.33 | -0.87% |
| 32 | 683.48 | 706.33 | 3.24% |
| 64 | 1251.60 | 1346.33 | 7.04% |

Run with `cmd/setbench`.

### LIST

| Elements/list | Packed B/list | Snug B/list | Redis B/list | Snug memory saving |
|---:|---:|---:|---:|---:|
| 1 | 21 | 128.56 | 92.03 | -39.70% |
| 4 | 72 | 170.84 | 146.33 | -16.75% |
| 8 | 140 | 255.05 | 226.33 | -12.69% |
| 16 | 276 | 389.56 | 386.33 | -0.84% |
| 32 | 548 | 683.82 | 706.33 | 3.19% |
| 64 | 1092 | 1251.95 | 1346.33 | 7.01% |

SL1 itself is compact; the 1–8 element deficit is mostly shared fixed per-key and
arena allocation overhead. No adaptive LIST physical format is planned for v1.

Run with `cmd/listbench`.

### ZSET

Structured 16-byte members after integer score delta encoding and member prefix
coding:

| Members/zset | Packed B/zset | Snug B/zset | Redis B/zset | Snug memory saving |
|---:|---:|---:|---:|---:|
| 1 | 22 | 128.00 | 98.34 | -30.17% |
| 4 | 53 | 166.34 | 162.33 | -2.47% |
| 8 | 93 | 212.38 | 258.33 | 17.79% |
| 16 | 174 | 296.60 | 450.33 | 34.14% |
| 32 | 336 | 454.86 | 834.33 | 45.48% |
| 64 | 659 | 782.73 | 1602.33 | 51.15% |

Before score compression, the same ZSET workload lost to Redis at every tested
size. Integer delta encoding moved 8+ members into positive territory, and member
front coding increased the 64-member saving to 51.15%. Both encodings are
adaptive and fall back when they do not reduce the representation.

Run with `cmd/zsetbench`, for example:

```sh
for n in 1 4 8 16 32 64; do
  go run ./cmd/zsetbench -keys 100000 -members "$n" -member-bytes 16
done
```

## Shared overhead observed in datatype runs

At 100,000 keys the repeated measurements show approximately:

- index reservation: ~31.5 B/key;
- common entry/key accounting: ~54–55 B/key;
- common entry struct: 40 bytes;
- index slot: 24 bytes.

This fixed cost explains why tiny containers can lose even when the packed payload
is smaller. The next memory optimization target is therefore the shared index/
entry/shard/arena overhead rather than new per-datatype encodings.

At around 1,000 keys, sparse-shard effects are much larger because active shards
reserve entry capacity and arena segments. Small-dataset measurements should not
be extrapolated from the 100k-key matrices.

## Realistic application workload matrix

The random 256-byte scalar case remains useful as an incompressible worst-case
control, but it is not intended to represent typical application data by itself.
`scripts/bench/compare-realistic-workloads.sh` runs the same Redis/SnugKV
black-box load, GET, mixed 90/10, and TTL harness across a deterministic profile
matrix:

| Profile | Size | Intended analogue |
|---|---:|---|
| `session-json` | 384 B | authenticated user/session/cache state with repeated field names |
| `api-json` | 768 B | cached API response/object with repeated schema |
| `cache-json` | 1024 B | typical cached GET request + nested response JSON with repeated application schema |
| `counter` | 10 B | canonical integer counters |
| `uuid` | 36 B | UUID identifiers stored as scalar values |
| `text` | 256 B | human/application text with recurring vocabulary |
| `repetitive` | 256 B | highly compressible control |
| `compressed` | 256 B | deterministic high-entropy binary with a gzip signature |
| `random` | 256 B | deterministic incompressible worst-case control |

The JSON profiles are valid fixed-size JSON documents, the counter and UUID
profiles use canonical encodings that exercise SnugKV's scalar codecs, and the
compressed profile intentionally carries an already-compressed signature so the
optimizer can exercise its recompression-avoidance path.

The default realistic comparison is intentionally lightweight and isolated: it
runs one database server at a time, compares Redis with optimized SnugKV, performs
one run per profile, and measures LOAD + pipelined GET. This avoids keeping three
large database processes resident together and avoids making expensive sequential
mixed/TTL tests part of every routine comparison.

```sh
KEYS=1000000 \
GET_OPS=2000000 \
WORKERS=8 \
PIPELINE=256 \
RUNS=1 \
bash scripts/bench/compare-realistic-workloads.sh
```

For routine engineering work and public reproducibility, prefer one profile at a
time against an already-running Redis-compatible server. The benchmark client does
not start, stop, kill, inspect, or configure server processes or containers.

```sh
bash scripts/bench/bench-one.sh uuid -p 6390
bash scripts/bench/bench-one.sh counter -p 6383 -s snug-opt
bash scripts/bench/bench-one.sh cache-json -p 6379 -s redis
```

The default dataset is 1,000,000 keys and 2,000,000 pipelined GETs. The script
builds only the local `rediswirebench` client, connects to the supplied host/port,
runs `FLUSHDB`, LOAD, then GET, and saves machine-readable JSON output. Start the
target Redis or SnugKV process yourself before running the benchmark.

Useful options include `-h/--host`, `-s/--server`, `-k/--keys`,
`-g/--get-ops`, `-w/--workers`, `-P/--pipeline`, and `--settle-ms`.

For deeper diagnostics, opt in explicitly:

```sh
SERVERS="redis snug-opt snug-raw" \
WORKLOADS="load get mixed ttl" \
RUNS=3 \
MIXED_OPS=1000000 \
TTL_OPS=1000000 \
bash scripts/bench/compare-realistic-workloads.sh
```

Only one selected server remains resident during its measurements. The default
post-load optimizer settle is a fixed 10 seconds rather than an open-ended
stability wait. Each profile writes raw JSON measurements and the suite writes a
combined `matrix-summary.json`. Treat these profiles as representative synthetic
workloads, not measurements of a specific production application. For product
claims, pair them with traces or distributions from an actual deployment when
available.

### Realistic-workload development snapshot — 2026-09-21

The following values are engineering snapshots from the current 4-logical-CPU
development machine. They are useful for regression tracking, but are not
universal Redis/SnugKV claims and should not replace fresh isolated multi-run
measurements for publication.

| Profile | Server | Best SET/s | Best GET/s | Lowest B/key |
|---|---|---:|---:|---:|
| counter · 10 B | Redis | 408,054 | 626,618 | 72.39 |
| counter · 10 B | SnugKV raw | 508,849 | 872,047 | 112.15 |
| counter · 10 B | SnugKV opt, pre-entry-compaction result | 402,402 | 735,830 | 77.13 |
| session JSON · 384 B | Redis | 285,118 | 434,121 | 520.39 |
| session JSON · 384 B | SnugKV raw | 383,040 | 782,087 | 544.37 |
| session JSON · 384 B | SnugKV opt | 255,277 | 563,998 | 272.89 |
| API JSON · 768 B | Redis | 221,241 | 371,244 | 968.39 |
| API JSON · 768 B | SnugKV raw | 296,125 | 663,780 | 909.90 |
| API JSON · 768 B | SnugKV opt | 188,607 | 491,242 | 273.00 |
| cache JSON · 1024 B | Redis | 192,063 | 339,800 | 1352.39 |
| cache JSON · 1024 B | SnugKV raw | 241,822 | 604,901 | 1245.78 |
| cache JSON · 1024 B | SnugKV opt | 156,941 | 434,474 | 568.37 |

For the counter profile, the current compacted engine state is smaller than the
77.13 B/key load result shown above. An explicit `SNUG.COMPACT` on the same
1,000,000-key dataset reduced accounted bytes from 77,181,376 to 72,605,632,
or 72.61 B/key. The compacted accounting was:

- index reservation: 33,605,632 bytes;
- entry/key storage: 39,000,000 bytes;
- metadata: 0 bytes;
- arena reservation/payload/live blocks: 0 bytes.

The corresponding recorded Redis reference was 72.39 B/key. The normal benchmark
convergence path did not yet trigger this final dense-entry compaction
automatically, so 72.61 B/key must be described as an explicit-compaction result,
not as the default post-load benchmark result.

The counter memory progression during this tuning phase was 103.86 B/key before
inline tiny scalars and compact stored entries, 86.66 B/key after inline scalar
storage, 77.13 B/key after the 24-byte stored-entry layout, and 72.61 B/key after
dense entry-capacity compaction.

## Benchmark discipline

For public performance claims, rerun Redis from a fresh dedicated instance or
database, record machine/configuration details, repeat runs to measure variance,
and distinguish engine-accounted memory, Redis `used_memory`, and process RSS.
Load-time comparisons from these harnesses are not product latency claims because
SnugKV is loaded in-process while Redis is reached over TCP.
