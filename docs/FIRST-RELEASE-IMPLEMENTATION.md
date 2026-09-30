# First Release Implementation Plan

This file is the detailed execution queue for `FIRST_RELEASE.md`.

It is intentionally operational. Keep high-level product scope in
`FIRST_RELEASE.md`; keep exact implementation slices, acceptance criteria,
evidence, and completion state here.

---

## 0. Rules for working this file

### One slice at a time

Each implementation slice should have:

- one clearly defined compatibility gap;
- one branch;
- focused tests;
- Redis reference evidence where applicable;
- one PR;
- documentation update;
- completion evidence recorded here.

### Required evidence for mutating commands

A new or changed mutating command must be checked for:

- direct behavior and errors;
- TTL semantics;
- WRONGTYPE behavior;
- maxmemory / OOM behavior;
- WATCH invalidation;
- MULTI / EXEC behavior;
- AOF / snapshot restart semantics;
- ACL command/key authorization;
- COMMAND metadata and key discovery;
- replication propagation;
- Cluster slot/cross-slot behavior where applicable;
- RESP2 / RESP3 reply shape where applicable.

### No speculative feature work

If a missing Redis command is not used by a supported client/workflow and does not
materially improve the product, classify it DEFER and move on.

---

# Phase A — Build the release compatibility inventory

## A1. Generate implemented-command inventory

Create a machine-readable inventory from SnugKV's registered command metadata.

Required fields:

- command name;
- subcommand name if applicable;
- arity;
- flags;
- ACL categories;
- key-spec/key extraction behavior;
- read/write classification;
- cluster behavior;
- implementation source location where practical.

Output:

- `docs/release/IMPLEMENTED-COMMANDS.md`
- optional JSON artifact for tooling.

Acceptance:

- inventory is generated from implementation metadata, not manually typed;
- command count matches `COMMAND COUNT`;
- parent/subcommand treatment is deterministic.

Status: **COMPLETE**

Implementation branch: `release/a1-command-inventory`

Retained outputs:

- `docs/release/IMPLEMENTED-COMMANDS.md`
- `docs/release/implemented-commands.json`

Inventory result:

- **374** top-level commands
- **36** structured subcommands
- top-level count is sourced from the same `commandTable` used by `COMMAND COUNT`

Generator:

```sh
bash scripts/release/generate-command-inventory.sh
```

Validation:

```sh
go test ./internal/server -run '^TestCommandInventory' -count=1 -v
go test ./cmd/commandinventory -count=1
go vet ./cmd/commandinventory ./internal/server
```

All validation passed on the retained generated inventory.

---

## A2. Build Redis 8.2 comparison inventory

Capture the Redis 8.2 command surface from a live Redis oracle.

For each Redis command/subcommand classify:

- implemented;
- intentionally unsupported;
- candidate gap;
- irrelevant to SnugKV architecture;
- Redis-internal/module-only.

Output:

- `docs/release/REDIS-COMMAND-GAP-AUDIT.md`

Acceptance:

- every candidate gap has an explicit reason;
- no gap is promoted to REQUIRED solely because Redis implements it.

Status: **COMPLETE**

Implementation branch: `release/a2-redis82-command-gap-audit`

Oracle/generator:

```sh
bash scripts/release/generate-redis82-gap-audit.sh
```

This starts an isolated Redis 8.2 container, captures its live `COMMAND` metadata,
and compares top-level commands and structured subcommands against the retained
SnugKV A1 inventory.

Classification result: **49 IMPLEMENTED**, **10 INTENTIONALLY_UNSUPPORTED**, **58 CANDIDATE_GAP**, **72 IRRELEVANT**. IMPLEMENTED entries represent structured COMMAND metadata gaps rather than missing behavior. Final REQUIRED / USEFUL / DEFER decisions happen in Phase A4 after client/workflow evidence.

---

## A3. Real-client command tracing

Exercise supported client libraries against Redis and/or SnugKV while tracing the
commands they issue during realistic workflows.

Minimum clients:

- ioredis
- node-redis
- redis-py
- go-redis

Minimum workflows:

- initial connection / handshake;
- pool creation;
- reconnect;
- basic GET/SET;
- transaction;
- Pub/Sub;
- RESP3 where supported;
- tracking/caching where supported;
- Cluster connection;
- MOVED/ASK routing;
- failover/reconnect.

Capture commands that clients issue automatically, especially tooling commands
such as HELLO, CLIENT, COMMAND, CLUSTER, INFO, ROLE and CONFIG.

Output:

- `docs/release/CLIENT-COMMAND-TRACE.md`

Status: **COMPLETE**

Implementation branch: `release/a3-client-command-tracing`

Trace coverage:

- ioredis
- node-redis
- redis-py
- go-redis
- RESP2
- RESP3
- reconnect
- transactions
- Pub/Sub
- client-side tracking/caching
- Cluster routing
- Cluster client startup/refresh commands

Standalone capture uses a transparent RESP proxy against Redis 8.2. Cluster capture
reuses the existing SnugKV client smoke and records each node with MONITOR.

The A3 output is `docs/release/CLIENT-COMMAND-TRACE.md`.

Retained result:

- **58** A2 candidate commands tested against supported workflows
- **0** candidate commands observed
- ioredis, node-redis, redis-py, and go-redis standalone/Cluster smoke passed
- persistent ioredis and node-redis clients survived automatic primary failover
- A3 uncovered and fixed `CLUSTER SLOTS` replica advertisement, which was a
  genuine Cluster client failover compatibility defect

---

## A4. Classify gaps

Create the authoritative queue:

| Gap | Classification | Evidence | Implementation slice |
|---|---|---|---|
| TBD | REQUIRED / USEFUL / DEFER | client/oracle/reason | branch/PR |

Definitions:

### REQUIRED

A first-release supported workflow or common client breaks without it.

### USEFUL

Not required for correctness, but materially improves adoption, operability, or
drop-in usability.

### DEFER

Low-value, architectural mismatch, obscure, undefined/incidental, or not used by
the supported release workflows.

Output:

- `docs/release/FIRST-RELEASE-GAPS.md`

Status: **COMPLETE**

Implementation branch: `release/a4-first-release-gap-classification`

Authoritative outputs:

- `docs/release/FIRST-RELEASE-GAPS.md`
- `docs/release/first-release-gaps.json`

Result:

- **0 REQUIRED** commands among the 58 Redis-only A2 candidates
- **9 USEFUL** commands retained as optional low-risk compatibility work
- **49 DEFER** commands excluded from first-release scope
- **49 already-implemented subcommands** still need structured `COMMAND`
  metadata completion as one REQUIRED compatibility slice
- `CLUSTER SLOTS` replica advertisement was a REQUIRED compatibility defect
  discovered and completed during A3

---

# Phase B — Implement required command gaps

This phase is populated from A4.

Do not pre-fill speculative commands here.

## B1. Complete structured COMMAND subcommand metadata

**Classification:** REQUIRED  
**Evidence:** A2 found 49 Redis subcommands whose behavior already exists in SnugKV
but is absent from structured command metadata.  
**Branch:** `release/b1-command-subcommand-metadata`  
**PR:** #257

Scope:

- add metadata only for behavior that already exists;
- do not implement new Redis commands in this slice;
- ensure A1 inventory includes the newly advertised leaves;
- compare the resulting structured metadata against the Redis 8.2 audit again;
- keep top-level `COMMAND COUNT` semantics unchanged.

Acceptance:

- all A2 entries classified IMPLEMENTED no longer appear as Redis-only metadata gaps;
- command inventory generation remains deterministic;
- focused COMMAND INFO/LIST/DOCS tests pass;
- A1/A2 retained artifacts regenerate cleanly.

Status: **COMPLETE**

Implementation notes:

- metadata-only slice; no Redis command behavior added;
- registers the 49 A2 `IMPLEMENTED` leaves under structured parent metadata;
- preserves top-level `COMMAND COUNT` semantics;
- regression tests lock all 49 leaves into the generated inventory.

Validation:

- top-level commands: **374** (unchanged)
- structured subcommands: **85** (36 + 49)
- Redis/Snug shared entries: **449** (was 400)
- Redis-only entries: **140** (was 189)
- A2 classification `IMPLEMENTED`: **0** (was 49)
- focused inventory/metadata tests: PASS
- `go vet ./internal/server ./cmd/commandinventory`: PASS

For every accepted gap use this template.

## Bx. <command / compatibility slice>

**Classification:** REQUIRED / approved USEFUL  
**Evidence:** link to release-gap audit  
**Branch:** TBD  
**PR:** TBD

Implementation checklist:

- [ ] parser / command registration
- [ ] COMMAND INFO / DOCS metadata
- [ ] ACL categories
- [ ] key extraction
- [ ] RESP2 tests
- [ ] RESP3 tests where relevant
- [ ] Redis differential tests
- [ ] MULTI / EXEC behavior
- [ ] WATCH behavior
- [ ] persistence / restart behavior
- [ ] replication behavior
- [ ] Cluster behavior
- [ ] error/arity compatibility
- [ ] docs updated

Status: **BLOCKED ON PHASE A**

---

# Phase C — Cluster onboarding decision

## C1. Audit first-time cluster bootstrap

Document the minimum current steps required to create a working multi-node
SnugKV cluster from clean processes.

Measure:

- number of config values;
- number of operator commands;
- whether node IDs/addresses must be copied manually;
- whether replicas require manual attachment;
- whether topology recovery after restart requires manual action.

Output:

- `docs/release/CLUSTER-ONBOARDING-AUDIT.md`

Status: **COMPLETE**

Implementation branch: `release/c1-cluster-onboarding-audit`

Audit result:

- routing-only clusters require the full static slot map on every node;
- three-node HA currently requires explicit peer/quorum/group configuration plus
  two post-start `REPLICAOF` attachments;
- node IDs do not require manual copying;
- restart/failover recovery is automatic once configured;
- `CLUSTER JOIN` and discovery seeds do not remove zero-state topology setup.

C1 recommendation: choose **C2 Option 2 — bounded bootstrap/admission**. The
needed work is a thin orchestration layer over existing hardened primitives, not
new consensus or service-discovery infrastructure.

---

## C2. Decide automatic admission scope

Choose exactly one:

### Option 1 — current explicit workflow is acceptable

Then:

- polish quickstart;
- provide scripts/example Compose setup;
- document recovery and removal.

### Option 2 — bounded automatic admission is required

Implement only enough automatic admission to make the supported first-release
topology easy to bootstrap.

Do not build a general service-discovery platform.

Status: **COMPLETE**

Implementation branch: `release/c2-bounded-cluster-bootstrap`

Selected: **Option 2 — bounded automatic admission/bootstrap**.

Implemented scope:

- exactly three-node first-release layouts;
- deterministic sharded slot generation;
- one-primary/two-replica HA configuration generation;
- generated peer lists, quorum, group/epoch, ACL, control-auth and persistence settings;
- automatic replica attachment through existing `REPLICAOF`;
- cluster coverage/transition validation;
- replication/failover health validation;
- redirected routing smoke;
- no SSH, remote process management, service discovery, or new consensus logic.

Retained operator doc: `docs/release/CLUSTER-BOOTSTRAP.md`.

Validation:

- `python3 scripts/release/test_snug_cluster_bootstrap.py`: **PASS** (4 tests)
- sharded three-process bootstrap: **PASS**
- sharded independent verification: **PASS**
- HA three-process bootstrap: **PASS**
- HA independent verification: **PASS**
- `bash scripts/release/run-cluster-bootstrap-smoke.sh`: **PASS**

---

# Phase D — Client compatibility matrix

Create an explicit release matrix.

| Client | Standalone | RESP3 | Pool/reconnect | Pub/Sub | Tracking | Cluster | Failover |
|---|---:|---:|---:|---:|---:|---:|---:|
| ioredis | TBD | TBD | TBD | TBD | TBD | TBD | TBD |
| node-redis | TBD | TBD | TBD | TBD | TBD | TBD | TBD |
| redis-py | TBD | TBD | TBD | TBD | TBD | TBD | TBD |
| go-redis | TBD | TBD | TBD | TBD | TBD | TBD | TBD |

Add other clients only if they materially improve release confidence.

Acceptance:

- no unexplained client failures;
- any unsupported behavior is documented;
- client-triggered missing commands feed back into Phase A/B.

Status: **COMPLETE**

Implementation branch: `release/d-client-compatibility-matrix`

Retained matrix: `docs/release/CLIENT-COMPATIBILITY-MATRIX.md`

Existing A3 evidence is retained for standalone, RESP3, reconnect, normal Cluster
routing, and ioredis/node-redis automatic failover. Phase D adds persistent
redis-py and go-redis failover clients and an all-four-client failover matrix
harness.

Validation:

- [x] `bash scripts/release/run-client-failover-matrix.sh`
- [x] ioredis persistent failover
- [x] node-redis persistent failover
- [x] redis-py persistent failover
- [x] go-redis persistent failover
- [x] old-primary restart/demotion/reparent
- [x] full cluster crash/restart chaos

go-redis/v9 9.22.0 is documented with a 1-second
`ClusterStateReloadInterval` for prompt hard-failover recovery; its default
periodic topology reload is 60 seconds.

---

# Phase E — Documentation convergence

Audit these against current source/tests:

- [x] README.md
- [x] COMPATIBILITY.md
- [x] KNOWN-LIMITATIONS.md
- [x] docs/PROJECT-STATE.md
- [x] PLAN.md
- [x] AI_AGENT_CONTEXT.md
- [x] issue #55 / active compatibility tracker

Known stale areas to verify:

- CLIENT tracking/caching/redirection;
- SCRIPT DEBUG status;
- Function dump/restore interoperability;
- Function allow-oom;
- dynamic ACL audit;
- legacy GEO aliases;
- distributed-system status;
- old single-node/no-failover statements.

Acceptance:

- no known contradiction between canonical docs;
- release boundary is stated consistently.

Status: **COMPLETE**

Implementation branch: `release/e-documentation-convergence`

Evidence:

- canonical-doc contradiction scan is clean for the known stale claims;
- CLIENT tracking/caching/redirection status converged;
- SCRIPT DEBUG, Function allow-oom, Function RDB dump/restore, dynamic ACL, and
  legacy GEO status converged with current source/tests;
- obsolete single-node/no-failover claims removed;
- distributed production-candidate boundary preserved;
- issue #55 synchronized with current implementation;
- A4 USEFUL-gap freeze disposition recorded in
  `docs/release/FIRST-RELEASE-GAPS.md`.

---

# Phase F — Feature freeze

Feature freeze is declared only after:

- [x] Phase A complete
- [x] all REQUIRED Phase B items complete
- [x] approved USEFUL Phase B items resolved
- [x] Phase C complete
- [x] Phase D green
- [x] Phase E complete

> **FIRST RELEASE FEATURE FREEZE IS ACTIVE**

The nine A4 USEFUL candidates are resolved for freeze: `CLIENT TRACKINGINFO`
behavior exists; the remaining eight are explicitly deferred to the post-release
compatibility backlog. No USEFUL candidate is approved as additional pre-freeze
feature work.

After this point, new functionality requires evidence that it fixes a
release-blocking compatibility defect.

Status: **ACTIVE**

---

# Phase G — Validation after freeze

## G1. Core gates

- [ ] `go test ./... -count=1`
- [ ] `go test -race ./... -count=1`
- [ ] `go vet ./...`
- [ ] RESP fuzz gate
- [ ] branch-specific differential suites

## G2. Durability

- [ ] AOF restart matrix
- [ ] snapshot restart matrix
- [ ] append failure behavior
- [ ] rewrite interaction
- [ ] transaction/script/function persistence
- [ ] replication restart behavior

## G3. Long-running validation

- [ ] 24-hour mixed workload soak
- [ ] retained error/leak/memory-growth evidence
- [ ] distributed soak
- [ ] failover repetition
- [ ] reshard/recovery repetition

## G4. Compatibility

- [ ] Redis live differential sweep
- [ ] client matrix rerun
- [ ] RESP2 regression
- [ ] RESP3 regression
- [ ] Cluster client smoke

---

# Phase H — Performance and memory optimization

Only optimize measured bottlenecks.

Current benchmark targets:

- [ ] retain fresh standalone Redis 8.2 baseline;
- [ ] large GEO benchmark;
- [ ] Lua compile/execute/cache benchmark;
- [ ] SORT external BY/GET benchmark under memory pressure;
- [ ] AOF mode performance;
- [ ] replication overhead;
- [ ] Cluster routing overhead;
- [ ] RSS vs engine-accounted memory analysis.

Optimization rule:

> No complexity increase without a reproducible benchmark showing the problem and
> a regression test/benchmark showing the improvement.

---

# Phase I — Packaging and first-user experience

## I1. Quickstart

A new user should be able to:

1. run SnugKV;
2. connect with redis-cli;
3. SET/GET;
4. try JSON;
5. try Search;
6. enable persistence;
7. run a replica;
8. launch a small cluster.

## I2. Packaging

- [ ] release Docker image
- [ ] versioned binary artifacts
- [ ] example config
- [ ] standalone Compose example
- [ ] replication example
- [ ] cluster example
- [ ] TLS example

## I3. Operational docs

- [ ] backup/restore
- [ ] upgrade/restart
- [ ] failover operation
- [ ] cluster reshard/removal
- [ ] persistence modes
- [ ] memory configuration
- [ ] security checklist

---

# Phase J — Release candidate

Release candidate requirements:

- [ ] feature freeze active
- [ ] all release gates green
- [ ] retained public benchmark evidence
- [ ] no known P0/P1 correctness defects
- [ ] docs consistent
- [ ] quickstart verified from clean machine/container
- [ ] known limitations explicit
- [ ] version/changelog/release notes prepared

When complete, cut the first public release.
