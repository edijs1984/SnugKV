# Progress

## Current milestone

SnugKV now has a broad single-node RESP2 command surface with native HASH, SET,
LIST, ZSET, and STREAM types, logical durability, memory accounting, adaptive
scalar encoding, observability, and operational tooling.

The current engineering focus has moved from building the core native datatype
set to finishing Redis compatibility families and validating release behavior.
The main remaining application-level gaps are Pub/Sub, transactions/WATCH,
HyperLogLog, GEO, scripting/functions, RESP3, and client/tooling compatibility.
Streams are now broadly implemented through Redis 8.2 reference-policy behavior;
a final differential edge-case audit remains useful but no known core Streams
command-family gap is currently tracked.

## Recently completed

### Streams

- Native versioned STREAM storage with backward decode.
- `XADD`, `XLEN`, `XRANGE`, `XREVRANGE`, `XDEL`, `XTRIM`.
- `MAXLEN` and `MINID` trimming, including XADD trimming and LIMIT-bounded trims.
- Redis 8.2 `KEEPREF`, `DELREF`, and `ACKED` reference policies.
- `XDELEX` and `XACKDEL` per-ID status behavior across multiple consumer groups.
- `XREAD` with blocking wakeup, disconnect cancellation, and shutdown cancellation.
- Durable consumer groups: `XGROUP CREATE/DESTROY/SETID/CREATECONSUMER/DELCONSUMER`.
- `XREADGROUP`, `XACK`, `XPENDING`.
- `XCLAIM` and `XAUTOCLAIM`, including ownership transfer, retry counters,
  `JUSTID`, `FORCE`, idle gating, and deleted-PEL cleanup.
- `XINFO STREAM`, `GROUPS`, `CONSUMERS`, and `HELP`.
- Persisted `entries-added`, `max-deleted-entry-id`, and distinct consumer
  attempted/successful interaction timestamps for accurate `idle`/`inactive`.
- STREAM excluded from the generic scalar optimizer after production-config
  testing exposed and fixed a corruption path.
- Export/restore, TTL, rename, WRONGTYPE, race, RESP, redis-cli smoke, and
  reference-policy coverage.

### Sparse-memory optimization

Canonical workload:

```text
keys=1000
value_bytes=16
shards=256
```

Original accounted memory:

```text
527,768 bytes
527.77 B/key total
```

Current verified result:

```text
179,224 bytes
179.22 B/key total
130,072-byte dynamic delta
```

Improvement:

```text
348,544 bytes saved
66.04% lower accounted memory
```

Current measured layout in that benchmark:

```text
index_slot_bytes                16
entry_struct_bytes              32
shard_struct_bytes             192
arena segment descriptor        24 bytes
entry_capacity_after          1184
entry_slots_used              1000
entry_free_slots               184
index_reserved_bytes         71040
entry_bytes                  52888
arena_bytes                  55296
```

The reduction came from structural accounting fixes, embedded/compact index
metadata, smaller sparse index stages, full 4-slot tiny tables plus a zero-size
negative lookup filter, staged entry growth, smaller/lazy arena allocation, entry
TTL sidecars, and compact arena segment descriptors.

## Broader completed surface

- Bounded streaming RESP2 parsing with fragmentation, pipelining, binary payloads,
  request limits, deadlines, connection limits, and graceful shutdown.
- Sharded collision-safe indexing, segmented arenas, expiration, compaction,
  explicit max-memory accounting, OOM rollback, and sampled LRU eviction.
- String, numeric, bit, expiration, key, JSON, and administration commands.
- Native HASH, SET, LIST, and ZSET with broad Redis-style command coverage.
- Blocking LIST/ZSET/STREAM waits register before readiness checks and use
  waiter/wakeup signaling instead of polling.
- Linux TCP peer-disconnect monitoring cancels blocked commands without consuming
  queued RESP bytes.
- Logical AOF/snapshot persistence with checksums, restart recovery,
  truncated-final-frame handling, corruption rejection, and online AOF rewrite.
- Prometheus metrics, separate loopback-only administration listener, Docker,
  Make targets, CI, benchmark harnesses, and soak tooling.

## Current verification discipline

Every feature branch is expected to pass:

```text
go test -race -count=1 ./...
go vet ./...
go test ./internal/resp -run '^$' -fuzz FuzzReadCommand -fuzztime=10s
```

Important durable/multi-key/native-type work also receives focused restart,
redis-cli/TCP, TTL, WRONGTYPE, OOM/rollback, and production-configuration tests as
appropriate.

Recent real-server Stream verification includes:

- `RETRYCOUNT 0` surviving encode/decode and subsequent stream operations;
- optimizer-enabled operation without stream corruption;
- `XAUTOCLAIM` returning deleted PEL IDs and cleaning them up;
- exact lifetime `entries-added` and `max-deleted-entry-id` after delete/trim histories;
- `XTRIM MINID ... LIMIT` behavior;
- consumer `idle` dropping after an empty read attempt while `inactive` continues
  from the last successful delivery;
- multi-group `KEEPREF` / `DELREF` / `ACKED` behavior for trimming/deletion,
  including dangling PEL cleanup through `XDELEX` / `XACKDEL`.

## Native datatype benchmark snapshot

The existing 100k-key benchmark tables remain in `benchmarks/README.md`. Recorded
results show the packed container payloads become increasingly competitive as
collection cardinality grows, while tiny collections can still pay more fixed
per-key overhead than Redis.

Do not turn single runs into universal latency claims; use paired/multi-run tests
when evaluating CPU tradeoffs.

## Remaining engineering work

1. Pub/Sub.
2. Transactions / WATCH.
3. HyperLogLog.
4. GEO.
5. Scripting / Functions scope.
6. RESP3 and CLIENT/CONFIG/ACL/COMMAND tooling compatibility.
7. Differential Redis edge-case audit for the completed Streams surface.
8. Fresh release-scale benchmarks, multi-run variance, million-record datasets,
   broader client compatibility, and retained long-duration soak evidence.
9. Distributed features only after the single-node target is mature.

See `PLAN.md`, `COMPATIBILITY.md`, `KNOWN-LIMITATIONS.md`, and GitHub issue #55.
