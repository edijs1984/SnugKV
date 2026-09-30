# SnugKV First Release

> This is the authoritative high-level plan for the first public SnugKV release.
> If an older TODO, audit, issue, or roadmap item conflicts with this file about
> first-release scope, follow this file and the current implementation/tests.

**Status:** Feature-completion phase  
**Target:** First public alpha / production-candidate release  
**Feature-freeze rule:** Once the implementation queue in
`docs/FIRST-RELEASE-IMPLEMENTATION.md` is complete, no new product features are
added until the release validation cycle is complete.

---

## 1. Release goal

The first release should be interesting enough that a Redis user can realistically
try SnugKV for:

- ordinary application caching and key/value workloads;
- native Redis-style collections;
- JSON and Search workloads;
- probabilistic and time-series workloads;
- Lua / Functions / transactions / Pub/Sub;
- persistence;
- primary/replica operation;
- automatic failover;
- Redis Cluster-style sharding and resharding.

The goal is **not** exhaustive Redis parity.

The release should instead provide a broad, useful, clearly documented and
well-tested compatibility surface with explicit boundaries.

---

## 2. Current product surface

The following major areas are already implemented and are not considered missing
first-release feature families.

### Core datatypes

- STRING / counters / bit operations / expiration
- HASH
- SET
- LIST
- ZSET
- STREAM + consumer groups
- HyperLogLog
- GEO

### Extended data capabilities

- JSON + JSONPath
- Search:
  - TEXT
  - TAG
  - NUMERIC
  - GEO
  - VECTOR
  - aggregation
- Bloom filter
- Cuckoo filter
- Count-Min Sketch
- TopK
- t-digest
- TimeSeries

### Application/runtime features

- MULTI / EXEC / WATCH
- classic and sharded Pub/Sub
- Lua scripting
- SCRIPT DEBUG audited surface
- Redis Functions
- SORT / SORT_RO
- COPY
- DUMP / RESTORE
- MIGRATE
- AUTH / ACL
- CLIENT tooling and client-side tracking/caching
- COMMAND / CONFIG tooling
- RESP2 + RESP3

### Durability and distributed operation

- AOF
- snapshots
- Redis RDB interoperability across the audited surface
- replication
- partial resynchronization
- authentication / TLS / mTLS
- automatic failover
- quorum leases and write fencing
- 16,384-slot Redis Cluster routing
- MOVED / ASK / ASKING / CROSSSLOT / CLUSTERDOWN
- reshard / rebalance / recovery
- explicit cluster membership
- replica-aware topology
- internal cluster-control authentication

---

## 3. Remaining first-release feature work

Only work that materially improves first-use viability belongs here.

### R1 — Command and client gap audit

Perform a systematic audit of:

1. commands SnugKV already implements;
2. commands Redis 8.2 exposes;
3. commands actually exercised by common Redis client libraries and application
   frameworks;
4. stale documentation that incorrectly reports already-implemented features as
   missing.

Every missing item must be classified as:

- **REQUIRED** — common clients or first-release workflows depend on it;
- **USEFUL** — materially improves first-release usability;
- **DEFER** — obscure, redundant, implementation-specific, or low-value for the
  release.

Only REQUIRED and explicitly approved USEFUL items enter the implementation queue.

### R2 — First-release command gaps

Implement the REQUIRED command gaps discovered by R1.

Do not implement commands merely to increase a parity percentage.

Each new command/slice requires:

- Redis 8.2 oracle behavior where applicable;
- RESP2/RESP3 behavior where applicable;
- ACL/COMMAND metadata;
- transaction behavior;
- persistence behavior if mutating;
- cluster key extraction / slot rules if applicable;
- tests and documentation.

### R3 — Cluster onboarding

Decide whether the current explicit membership workflow is simple enough for a
new user.

If not, add a bounded automatic admission/bootstrap path for the supported
cluster topology.

The goal is **easy first deployment**, not a general-purpose service discovery
system.

### R4 — Real-client compatibility expansion

Validate realistic flows for at least:

- ioredis
- node-redis
- redis-py
- go-redis

Then add additional widely used clients/frameworks when practical, prioritizing
clients that expose real compatibility gaps.

Important flows:

- normal commands;
- connection pooling;
- reconnect;
- RESP3;
- transactions;
- Pub/Sub;
- client-side tracking/caching;
- Cluster routing;
- failover/reconnect.

### R5 — Documentation convergence

Before feature freeze, synchronize:

- README.md
- COMPATIBILITY.md
- KNOWN-LIMITATIONS.md
- PLAN.md
- docs/PROJECT-STATE.md
- AI_AGENT_CONTEXT.md
- issue #55 or its successor tracker

No release document may claim a feature is absent when current tests/source show
it is implemented.

---

## 4. Explicitly deferred from the first release

The following do **not** block feature freeze unless R1 discovers a real client or
workflow dependency:

- exhaustive Redis command parity;
- exhaustive Redis Cluster parity;
- multiple logical databases;
- cross-database COPY semantics;
- RESP3 attribute frames not required by supported commands;
- additional CLIENT classes without a real corresponding connection mode;
- locale-sensitive non-ASCII SORT ALPHA parity;
- Redis Module payload compatibility;
- permanent GEO indexing unless benchmarks demonstrate a need;
- automatic discovery systems beyond the bounded cluster-onboarding requirement;
- every Redis implementation-detail diagnostic;
- perfect incidental ordering where Redis itself does not define stable semantics.

---

## 5. Feature-freeze point

Feature freeze begins when all of the following are true:

- R1 command/client gap audit is complete;
- every REQUIRED command gap is implemented;
- approved USEFUL gaps are either implemented or explicitly deferred;
- cluster onboarding decision is complete;
- first-release client matrix is green;
- documentation is internally consistent.

At feature freeze:

> **No new datatypes, commands, Search features, cluster features, or convenience
> features are added unless they fix a release-blocking compatibility defect.**

All work moves to validation and optimization.

---

## 6. Post-freeze release work

After feature freeze, priorities are:

1. full project test/race/vet/fuzz gates;
2. Redis differential compatibility sweeps;
3. restart/durability/failure injection;
4. 24-hour mixed workload soak;
5. replication/failover/Cluster soak;
6. public Redis-vs-SnugKV baseline retention;
7. CPU/latency profiling;
8. memory/RSS profiling and optimization;
9. large GEO / Lua / SORT targeted benchmarks;
10. packaging and Docker ergonomics;
11. security/configuration review;
12. quickstart and deployment examples;
13. release candidate validation.

No benchmark result is a substitute for correctness testing.

---

## 7. Release claim boundary

The first release may be described as:

> **Alpha / production-candidate for the documented and audited surface.**

Do not describe it as:

- a complete Redis replacement;
- exhaustive Redis/Redis Stack parity;
- production-proven at large scale;
- a general-purpose consensus database.

---

## 8. Working-file authority

For first-release work, use this order:

1. current implementation + tests;
2. `FIRST_RELEASE.md` — release scope and stage;
3. `docs/FIRST-RELEASE-IMPLEMENTATION.md` — detailed execution queue;
4. `docs/PROJECT-STATE.md` — canonical current project state;
5. `PLAN.md` — broader roadmap;
6. `PROGRESS.md` — chronological evidence;
7. `COMPATIBILITY.md` / `KNOWN-LIMITATIONS.md` — public boundaries;
8. focused audit documents;
9. older TODO/spec files and historical issues.

---

## 9. Current phase

**Current phase: R1 — command and client gap audit.**

The public standalone benchmark work may continue in parallel because it is
measurement infrastructure, not new feature scope.

All implementation work from this point should be selected through the
first-release gap audit rather than by picking arbitrary unchecked roadmap items.
