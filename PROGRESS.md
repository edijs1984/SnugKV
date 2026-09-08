# Progress

## Current milestone

The single-node SnugKV implementation is complete for the command, storage,
encoding, optimizer, persistence, observability, and packaging scope described by
the specification. Release-scale benchmarking and a 24-hour soak remain external
validation activities because they require dedicated time and hardware.

## Completed

- Bounded streaming RESP2 parsing with fragmentation, pipelining, binary payloads,
  request limits, deadlines, connection limits, and graceful shutdown.
- The documented Redis-compatible command subset, including strict SET options,
  multi-key operations, counters, expiration, introspection, and MORPH admin commands.
- Collision-safe sharded open-addressed indexes, segmented value arenas, indexed
  expiration heaps, exact byte ownership, and bounded cleanup.
- Explicit index, arena, entry, and schema reservations with atomic OOM rollback,
  memory audits, compaction, and sampled allkeys/volatile LRU eviction.
- Raw, integer, UUID, timestamp, JSON-shape, dictionary, LZ4, and Zstandard codecs
  with exact reconstruction and raw fallback.
- A budgeted optimizer with heat classification, bounded queues and scratch space,
  byte-rate/duty-cycle limits, hysteresis, and stale-version rewrite rejection.
- Checksummed logical AOF and snapshot persistence, truncation/corruption handling,
  exclusive data-directory locking, fsync policies, recovery, and online AOF rewrite.
- Prometheus metrics and a separate loopback-only administration listener.
- Strict JSON configuration plus environment and flag overrides, container files,
  Make targets, CI, operational documentation, ADRs, and a benchmark harness.

## Verification

The project uses `/home/edijs/Downloads/go1.27.1.linux-amd64/go/bin/go` because the
default PATH toolchain is Go 1.17.2. Tests use `GOCACHE=/tmp/snugkv-go127-cache`,
`GOPATH=/tmp/snugkv-gopath`, and `-buildvcs=false` because this workspace has no
usable Git metadata.

- The final full unit/integration suite and full race suite passed on 2026-09-07.
- Final `go fmt ./...` and `go vet ./...` checks passed.
- RESP, codec, JSON-shape, persistence, and decode fuzz targets each passed a
  10-second run, with 60,000 to 112,000 executions per target.
- A built server passed redis-cli command checks and a fragmented binary round trip.
- A real AOF restart preserved values, counters, and a live expiration deadline.
- Separate administration and metrics listener integration tests passed.
- The mixed-temperature soak harness passed a two-second smoke run with 156,606
  verified reads, 1,566 writes, 156 TTL churn cycles, and zero mismatches.
- The production Dockerfile built successfully as `snugkv:local`; a disposable
  container passed redis-cli PING and SET/GET through its published port.

Benchmark commands and measured results are in [benchmarks/README.md](benchmarks/README.md).

## Measured results

On the recorded 10,000-key single runs, JSON-shape encoding reduced engine-accounted
memory by 43.1% for session JSON and 35.1% for API JSON. Random and gzip-wrapped
random inputs stayed raw with unchanged accounted memory. A deliberately repetitive
4 KiB synthetic workload reduced accounted memory by 91.0% with LZ4. These are
reproducible engineering measurements, not general performance claims.

## Remaining release validation

- Run the specification's million-record datasets and multi-run variance analysis
  on dedicated benchmark hardware, including a Redis RSS comparison.
- Run and retain evidence from the provided `make soak` 24-hour mixed-workload
  harness; a short smoke run is part of routine verification.
- Test third-party Redis client libraries beyond redis-cli and the protocol-level
  Go/Node coverage already exercised.
- Publish the validated container image when a target registry and credentials are
  available.

## Known environment limitation

The workspace's `.git` metadata is incomplete/read-only, so Git status and automatic
VCS stamping are unavailable. Builds use `-buildvcs=false`.
