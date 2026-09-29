# Cluster Production Hardening Audit

Date: 2026-09-29

## Scope

This document records the current SnugKV cluster/sharding production-hardening
state after Phases 19-21.

It complements:

- `docs/AUTOMATIC-FAILOVER-AUDIT.md`
- `docs/REPLICATION-TLS-AUDIT.md`
- `PLAN.md`
- `PROGRESS.md`
- `COMPATIBILITY.md`

The goal is to distinguish implemented distributed behavior from remaining
production-hardening work.

## Implemented cluster routing

SnugKV implements Redis-compatible hash-slot routing for the audited surface:

- 16,384 hash slots;
- Redis CRC16/hash-tag rules;
- `MOVED` redirects;
- `ASK` redirects;
- connection-scoped, one-shot `ASKING`;
- `CROSSSLOT` enforcement;
- `CLUSTERDOWN Hash slot not served` for unassigned slots;
- `CLUSTER SLOTS`;
- `CLUSTER NODES`;
- `CLUSTER SHARDS`;
- `CLUSTER INFO`.

Live TCP tests exercise MOVED, ASK/ASKING, CLUSTERDOWN, and routing changes after
ownership transfer.

## Resharding and rebalance

Implemented resharding primitives include:

- `CLUSTER SETSLOT STABLE`;
- `MIGRATING`;
- `IMPORTING`;
- `NODE`;
- `COUNTKEYSINSLOT`;
- `GETKEYSINSLOT`;
- cluster-aware `MIGRATE` / `RESTORE-ASKING`.

The higher-level rebalance flow includes:

- deterministic PLAN generation;
- DRYRUN;
- ONCE;
- bounded BATCH;
- multi-donor ALL coordination;
- STATUS;
- explicit RECOVER PLAN / RESUME;
- stale-plan and stale-coordinator rejection;
- local mutation serialization.

The source keeps the slot in MIGRATING state until remote topology convergence
has succeeded.

## Topology persistence and restart recovery

Cluster topology state is persisted in a dedicated sidecar when AOF or snapshot
persistence is configured.

Persisted state includes:

- cluster enabled state;
- node address;
- local topology epoch;
- known-node registry;
- slot owners;
- MIGRATING markers;
- IMPORTING markers.

Restart recovery preserves the ownership digest and active transition state.

`CLUSTER REBALANCE RECOVER RESUME` reasserts the target-side IMPORTING marker
before moving keys. This allows recovery when the source retained its durable
MIGRATING marker but the target lost or cleared transient transition state.

Automated coverage includes source restart during an interrupted slot migration
and subsequent successful recovery/finalization.

## Membership

The implemented membership surface includes:

- zero-slot known members;
- `CLUSTER JOIN`;
- guarded node removal/evacuation;
- membership recovery/convergence;
- persistence of known nodes;
- topology/ownership digest validation.

Membership and reshard mutation share the cluster rebalance operation guard where
required to avoid overlapping local topology mutations.

Fully automatic membership admission remains deferred.

## Replica-aware shard topology

Cluster topology overlays failover-group replica metadata on slot-owning masters.

Implemented behavior includes:

- replicas shown under their shard owner in `CLUSTER SHARDS`;
- replica/master relationships in `CLUSTER NODES`;
- replica discovery for remote shards;
- replicas excluded from rebalance capacity;
- role changes after failover ownership transfer;
- conservative node health observation.

Health semantics are deliberately conservative:

- `online`: locally known or successfully authenticated/validated during the read;
- `unknown`: unreachable, unverifiable, or wrong failover group;
- no speculative hard failure is inferred solely from an unavailable probe.

## Operator health and consistency

`CLUSTER HEALTH` summarizes:

- slot coverage;
- per-shard owner health;
- replica counts;
- online replicas;
- unknown replicas;
- overall healthy/unknown/fail state.

`CLUSTER CONSISTENCY` reports:

- current ownership digest;
- local topology epoch;
- slot coverage;
- active reshard transition;
- write-fencing state;
- fence reason;
- current lease holder and expiry.

The local topology epoch is explicitly not treated as a distributed consensus
epoch.

## Stale coordinator fencing

Remote rebalance execution and failover ownership convergence include the expected
slot-ownership digest.

A node rejects stale control operations when its current ownership digest no
longer matches the coordinator's view.

This avoids pretending the per-node `clusterTopologyEpoch` is a globally
synchronized configuration term.

## Failover / rebalance interaction

Failover ownership changes and rebalance mutations are serialized locally.

Failover ownership replacement also refuses active slot MIGRATING/IMPORTING
transitions.

This prevents a local failover ownership mutation from committing concurrently
with a slot move.

## Write fencing and quorum leases

For configured failover groups, both promoted leaders and the original primary
use majority-backed leases for write safety.

Behavior:

- a writable primary/master must hold a valid majority-backed lease;
- promoted leaders renew their lease before expiry;
- the original primary also establishes/renews a quorum lease;
- loss of quorum eventually fences writes after lease expiry;
- reads remain available while fenced;
- a node that has granted a live lease to another leader fences its own writes;
- conflicting leader leases are rejected while an existing lease is valid;
- after quorum returns, the current primary can reacquire a lease and become
  writable again.

This reduces split-brain write windows without claiming a general-purpose
consensus protocol for arbitrary cluster metadata.

## Authentication and TLS

Cluster control RPCs reuse the replication authentication credentials.

The internal rebalance data path now also reuses the TLS-capable upstream dialer.

When `mastertls` is enabled, internal rebalance/recovery MIGRATE traffic uses:

- CA verification;
- server-name verification;
- TLS 1.2 minimum;
- optional client certificate authentication;
- `masteruser` / `masterauth` through MIGRATE AUTH/AUTH2.

Public Redis-compatible `MIGRATE` intentionally remains on its normal TCP
semantics and does not silently inherit `mastertls`.

Automated tests include a TLS-only target that accepts internal cluster migration
with CA verification and AUTH.

## Known boundaries

The following are still open production-hardening items:

1. Internal-control authorization identity
   - Internal CLUSTER subcommands such as rebalance execution, failover-owner
     convergence, and membership control are carried over authenticated RESP.
   - They are still parsed by the public CLUSTER command surface.
   - A sufficiently privileged authenticated client is not yet distinguished by
     a dedicated internal-control session identity.
   - This should be fixed with an explicit internal control-channel/session
     mechanism rather than source-IP or username heuristics.

2. Broader chaos/soak
   - More multi-process partition matrices;
   - repeated kill/restart during migration/failover;
   - long-running partition/heal soak;
   - disk/persistence failure injection across distributed transitions.

3. Client-library cluster validation
   - redis-cli cluster routing is covered;
   - broader ioredis/node-redis/redis-py/go-redis Cluster-mode smoke remains.

4. Distributed membership policy
   - fully automatic admission remains optional/deferred;
   - current membership changes are explicit and guarded.

5. Performance
   - cluster routing, resharding, TLS migration, failover recovery, and topology
     observation need reproducible multi-node benchmark baselines before product
     performance claims.

## Validation

Focused race-enabled suites cover:

- TLS-only internal cluster migration;
- public MIGRATE transport separation;
- restart during active migration;
- recovery with lost target IMPORTING state;
- MOVED/ASK/ASKING/CLUSTERDOWN over real TCP clients;
- stale rebalance/failover coordinator fencing;
- failover/rebalance serialization;
- original-primary and promoted-leader lease fencing;
- quorum recovery.

Before merge, the project release gate remains:

```
go test -race -count=1 ./...
go vet ./...
```

plus RESP fuzz and any branch-specific compatibility checks required by the
changed surface.
