# Benchmark results

## Redis 8.2 comparison — 1M repetitive scalar keys, 2026-09-20

Black-box RESP2/TCP measurements on Linux amd64, Go 1.27.1, 4 logical CPUs,
1,000,000 keys, 256-byte deliberately repetitive values, 4 GET workers, and GET
pipeline depth 256. Redis and SnugKV were preloaded with the same logical
dataset. These are single-run development measurements; repeat runs are required
before making public performance claims.

### Memory after load

| Server/config | Reported memory | Bytes/key |
|---|---:|---:|
| Redis 8.2 | 393,260,912 B | 392.39 B/key load delta |
| SnugKV raw | 362,314,176 B | 362.27 B/key load delta |
| SnugKV optimized | 162,631,296 B | ~162.6 B/key accounted |

For this synthetic highly-compressible workload, optimized SnugKV used about
58.6% less reported memory than Redis while preserving all 1,000,000 logical
256-byte values. This percentage is workload-specific and must not be generalized
to incompressible data.

### Pipelined GET

| Server/config | GET ops/s | p50 | p95 | p99 |
|---|---:|---:|---:|---:|
| Redis 8.2 | 432,213 | 9.14 us | 12.72 us | 16.14 us |
| SnugKV raw | 526,951 | 6.93 us | 11.73 us | 17.41 us |
| SnugKV optimized, before response-copy fix | 404,451 | 8.43 us | 18.07 us | 27.83 us |
| SnugKV optimized, after response-copy fix | 430,233 | 8.05 us | 15.69 us | 25.28 us |

The GET percentile samples for pipelined runs are amortized per-operation batch
times, not independent request latencies. The response-copy optimization removed
the extra value-sized allocation/copy previously performed after decoding an
encoded value; optimized throughput improved by about 6.4% in the measured run.

At this point optimized SnugKV was within about 0.5% of the Redis throughput
measurement on this workload, while raw SnugKV remained faster. The remaining
optimized-vs-raw gap is under profiling and should not yet be attributed to one
component.

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

## Benchmark discipline

For public performance claims, rerun Redis from a fresh dedicated instance or
database, record machine/configuration details, repeat runs to measure variance,
and distinguish engine-accounted memory, Redis `used_memory`, and process RSS.
Load-time comparisons from these harnesses are not product latency claims because
SnugKV is loaded in-process while Redis is reached over TCP.
