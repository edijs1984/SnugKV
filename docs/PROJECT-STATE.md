# SnugKV Project State

**Updated:** 2026-09-29

This is the canonical current-state handoff for maintainers and coding agents.
Historical audit documents remain evidence for the state at the time they were
written; `PLAN.md` is the active roadmap and `PROGRESS.md` is the implementation
log.

## Current status

SnugKV is an alpha-stage Redis-compatible datastore in Go with a broad
single-node command surface and a substantial distributed implementation.

The distributed core is feature-complete enough that current work is primarily
**production hardening**, not foundational cluster implementation.

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

## Current distributed hardening queue

The distributed implementation has completed the major chaos, recovery,
client-library, and benchmark hardening gates.

Completed evidence includes:

- real multi-process crash/restart and partition/heal harnesses;
- interrupted source/target migration recovery, repeated recovery, persistence
  failure, and corrupted-replica recovery;
- ioredis, node-redis, redis-py, and go-redis Cluster-mode smoke;
- reproducible routing, reshard, TLS migration, failover-recovery, and
  topology-observation benchmarks;
- a one-cycle distributed soak smoke covering all seven recovery/failure cases.

The remaining release gate is:

1. **Final release audit**
   - retain a longer-duration distributed soak run;
   - audit operator runbooks and configuration examples;
   - refresh compatibility/limitations documentation against the implemented
     distributed behavior;
   - run the full release validation suite before merge.

Optional/deferred: fully automatic membership admission policy and richer
per-node health/TLS-SNI ergonomics.

## Production claim boundary

Do **not** describe the distributed system as production-complete yet.

The implemented cluster/replication/failover core is substantial, but the project
still needs broader independent multi-process failure testing, client-library
validation, recovery matrices, soak, and multi-node benchmarks before a
production-complete claim is justified.

"Production-candidate" is the target after those hardening gates are completed.

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
