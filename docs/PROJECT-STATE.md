# SnugKV Project State

**Updated:** 2026-10-10

This is the canonical current-state handoff for maintainers and coding agents.
For first-release scope and sequencing, use `FIRST_RELEASE.md` as the authoritative high-level plan and `docs/FIRST-RELEASE-IMPLEMENTATION.md` as the detailed execution queue.
Historical audit documents remain evidence for the state at the time they were
written; `PLAN.md` is the active roadmap and `PROGRESS.md` is the implementation
log.

## Current status

SnugKV is an alpha-stage Redis-compatible datastore in Go with a broad
single-node command surface and a substantial distributed implementation.

The distributed core has completed the current production-candidate hardening
gates for its audited scope. **First-release feature freeze is active.** Current
work is release validation, durability/failure injection, soak, profiling,
packaging, security review, and first-user/release-candidate preparation rather
than new product feature implementation.

Implemented distributed capabilities include:

- primary/replica replication with full sync and bounded-backlog partial PSYNC;
- Redis RDB full-sync interoperability across the audited encoding surface;
- authenticated and TLS replication, including verified server identity and mTLS;
- restart continuation for graceful replica restarts;
- automatic failover with majority election, durable voting, majority-backed
  leases, write fencing, reparenting, and returning-primary demotion;
- dynamic failover membership with joint-majority changes and durable recovery;
- Redis-compatible 16,384-slot routing, hash tags, `MOVED`, `ASK`, `ASKING`,
  `CROSSSLOT`, and `CLUSTERDOWN`;
- `CLUSTER SLOTS`, `NODES`, `SHARDS`, and `INFO`;
- guarded resharding, deterministic rebalance planning/apply/recovery, and
  restart-safe interrupted migration recovery;
- explicit cluster membership, safe node evacuation/removal, and membership
  recovery/convergence;
- replica-aware shard topology and failover ownership convergence;
- cluster health/consistency/operator diagnostics;
- stale-coordinator fencing and failover/reshard serialization;
- dedicated internal control-plane authentication using `cluster_control_auth`
  and connection-scoped `SNUG.INTERNAL AUTH`;
- TLS-capable authenticated internal migration.

SnugKV intentionally exposes one logical database.

## Changes since the 2026-10-03 freeze statement

The first-release feature freeze was declared on 2026-10-03. New capability work
has been merged since then, mostly on 2026-10-08 to 2026-10-10. The freeze rule
in `FIRST_RELEASE.md` is therefore relaxed for the items below; record any
further scope decision here. Details and measurements are in `CHANGELOG.md`.

- **Transactions and scripting:** `MULTI ATOMIC` and the `-atomic-transactions`
  option make `MULTI`/`EXEC` and writable scripts/functions all-or-nothing;
  `EXEC` snapshots only the keys it touches for verified commands. See
  `docs/ATOMIC-TRANSACTIONS.md`.
- **Built-in functions:** a `snug_*` function library (rate limiter, lock with
  fencing token, idempotency keys, bounded counter, reliable queue, leaderboard)
  loads at startup. See `docs/BUILTIN-FUNCTIONS.md`.
- **JSON:** RedisJSON reply shapes and multi-match updates for JSONPath queries.
- **TypeScript client:** `clients/typescript`, published as `@snugkv/client`.
- **Memory layout:** 8-byte index slots with a per-shard key log, 16-byte stored
  entries, hashes packed up to 128 fields, fixed-width sets front-coded up to 128
  members, cold list layout, idle trim and faster maintenance after writes,
  optimizer duty cycle and futile-attempt thinning. See `docs/memory-format.md`.
- **Blockchain-shaped data:** exact codecs for hex (ID 13), base58 (14), uint256
  decimal (15) and Solana SPL token accounts (16); hex keys stored in binary in
  the key log. Hex and token accounts convert on the write path; base58 and
  uint256 convert in the background optimizer; signatures stay raw.
- **`rpccache`:** a caching JSON-RPC proxy for Solana and EVM nodes with a
  Redis-protocol backend (`cmd/rpccache`, `cmd/rpcbench`). See
  `docs/RPC-CACHE.md`. It is a separate program; the server itself is unchanged. `-auth` adds API keys, plans, rate limits, daily quotas and per-class usage counts kept in SnugKV, managed with `cmd/rpckeys`.
- **Benchmark profiles:** `eth-hash`, `eth-address`, `sol-pubkey`,
  `sol-signature`, `uint256`, `sol-token-account`, `sol-token-account-b64`,
  `hex-key` and `address-key`, in the wire bench, the bench scripts and the
  Benchmark Lab.

Most of the memory changes above are marked "pending live benchmark" in the
changelog; sandbox and Benchmark Lab figures are recorded in
`benchmarks/README.md`.


## Adaptive storage status

The current first-release storage model is one adaptive SnugKV mode rather than
separate raw/optimized products. Logical Redis datatypes remain unchanged while
physical representations adapt to workload and data.

Native HASH is now frozen for this release pass:

- small/medium HASHes remain COLD/indexed;
- large HASHes become HOT only at 256+ fields when the pipelined write path
  demonstrates an active mutation workload;
- active HOT HASHes are skipped by generic compaction;
- after the HOT idle window, maintenance freezes them back to indexed COLD
  storage and reclaims the temporary mutable-sidecar footprint;
- incremental pipelined growth is regression-covered so the threshold cannot be
  bypassed by an early thaw.

Current validated SnugKV 1M-item baselines on the development host are
467,881 WRITE/s and 531,098 READ/s for hash-100, and 501,726 WRITE/s and
488,073 READ/s for active hash-1000. The large active footprint was 141.50
bytes/item and settled to approximately 109.43 bytes/item after HOT -> COLD.
These are workload-specific engineering baselines, not universal performance
claims.


## Current distributed hardening queue

The distributed implementation has completed the major chaos, recovery,
client-library, and benchmark hardening gates.

Completed evidence includes:

- real multi-process crash/restart and partition/heal harnesses;
- interrupted source/target migration recovery, repeated recovery, persistence
  failure, and corrupted-replica recovery;
- ioredis, node-redis, redis-py, and go-redis Cluster-mode smoke plus persistent
  automatic-failover recovery for all four supported clients;
- reproducible routing, reshard, TLS migration, failover-recovery, and
  topology-observation benchmarks;
- repeated-recovery stress passed 5/5 focused runs with 2,000 durable keys;
- dedicated failover-restart stress passed 10/10 runs, including alternate
  elected-leader paths;
- the bounded distributed soak passed 6/6 cycles, 42/42 cases, with zero failures/timeouts over 952 seconds (~15m52s) after the failover stabilization harness race was fixed;
- an earlier extended soak completed 70 consecutive chaos cases before exposing
  the MIGRATE pipelined-read timeout bug that is now regression-covered.

The distributed production-candidate release gate is complete. The bounded
first-release bootstrap helper also generates and verifies the documented
three-node sharded and one-primary/two-replica HA layouts from one concise
topology declaration.

Final project-wide validation was operator-reported green on `main` after PR #249:

- `go test ./... -count=1`
- `go test -race ./... -count=1`
- `go vet ./...`

The retained distributed hardening evidence and documentation audit are therefore
closed for this milestone.

Optional/deferred: fully automatic membership admission beyond the bounded
three-node bootstrap helper, and richer per-node health/TLS-SNI ergonomics.

## Production claim boundary

Do **not** describe the distributed system as production-complete yet.

The implemented cluster/replication/failover core has now completed the planned
multi-process failure matrix, Cluster client-library smoke, distributed benchmark
baselines, repeated recovery stress, and retained bounded soak validation.

Do not interpret this as a claim of exhaustive Redis Cluster parity or mature
large-scale production history. The appropriate current distributed-system claim
is **production-candidate** for the audited scope.

## Documentation authority

When documents disagree, use this precedence:

1. implementation + tests;
2. this file (`docs/PROJECT-STATE.md`);
3. `PLAN.md` for active roadmap;
4. `PROGRESS.md` for chronological implementation evidence;
5. `COMPATIBILITY.md` and `KNOWN-LIMITATIONS.md` for user-facing boundaries;
6. focused audit documents under `docs/`;
7. `TODO.md` and the original sections of `SNUGKV_AGENT_SPEC.md` as historical planning material.

Do not infer current status from an old unchecked TODO item or an old audit's
"still deferred" section without checking newer documents/tests.

## Primary references

- `README.md`
- `COMPATIBILITY.md`
- `KNOWN-LIMITATIONS.md`
- `PLAN.md`
- `PROGRESS.md`
- `docs/CLUSTER-PRODUCTION-HARDENING.md`
- `docs/AUTOMATIC-FAILOVER-AUDIT.md`
- `docs/REPLICATION-TLS-AUDIT.md`
- `docs/memory-format.md`
- `docs/RPC-CACHE.md`
- `docs/operations.md`
- `SECURITY.md`

## Validation baseline

Before merging distributed-system changes, run at minimum:

```sh
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
```

Add focused compatibility, live Redis differential, fuzz, multi-process, and
failure-injection tests for the surface being changed.
