# Benchmark results

These are single-run engineering measurements from 2026-09-07 on Linux amd64,
Go 1.27.1, Intel Core i3-7020U (4 logical CPUs), GOMAXPROCS=4, 256 shards, seed 1,
and 10,000 records. Every read was checked against its input. Process heap includes
the harness; accounted bytes use the engine model. Commands are reproducible with
`go run -buildvcs=false ./cmd/snugbench` and the flags below.

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
for API JSON after template optimization and compaction. Session GET p99 was 1.22x
raw; API p99 was lower in this single run. Random and already-compressed inputs
remained raw and had the same engine-accounted footprint as raw mode. Heap results
and latency vary between runs, so these measurements are not universal claims.
The synthetic repeated 4 KiB workload selected LZ4 for all records and reduced
accounted memory by 91.0%; it is deliberately compressible and is not representative
of arbitrary application data.

The runs are smaller than the specification's million-record release dataset and
do not replace multi-run variance, Redis RSS comparison, or a 24-hour soak. Those
release-scale validations need dedicated hardware and time. The harness also
supports telemetry, counters, mixed, and compressible inputs for follow-up runs.
