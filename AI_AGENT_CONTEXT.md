# SnugKV — AI / Agent Project Context

> High-signal project brief for AI coding agents, contributors, reviewers, and "vibecoders".
> Read this first when you need to understand what SnugKV is capable of without
> walking every audit, benchmark, and implementation file.

**Updated:** 2026-10-10

> **Since 2026-09-30:** atomic transactions (`MULTI ATOMIC`, `-atomic-transactions`),
> the built-in `snug_*` function library, the TypeScript client, a smaller memory
> layout (8-byte index slots with a per-shard key log, 16-byte entries), exact
> codecs for hex, base58, uint256 and Solana token accounts (IDs 13 to 16), hex keys
> stored in binary, and the separate `rpccache` JSON-RPC proxy. See
> `docs/PROJECT-STATE.md` for the list and `CHANGELOG.md` for measurements.
> Memory figures further down predate the new layout.

## 1. What SnugKV is

SnugKV is an alpha-stage Redis-compatible in-memory datastore written in Go.

It is not a toy key/value server. The project currently combines:

- RESP2 and RESP3 protocol support;
- broad Redis command compatibility;
- native packed HASH / SET / LIST / ZSET / STREAM storage;
- first-class JSON and Redis-style JSONPath behavior;
- Redis Search-style indexing/query functionality;
- Lua scripting and Redis Functions support;
- transactions, optimistic locking, Pub/Sub, ACLs and tooling;
- AOF + snapshot persistence and restart recovery;
- memory accounting, expiration, eviction and adaptive optimization;
- replication and Redis RDB full-sync interoperability;
- automatic failover with quorum-backed leases and write fencing;
- Redis-compatible Cluster hash-slot routing;
- guarded resharding, recovery and explicit membership;
- multi-process chaos/recovery validation and Cluster client smoke tests.

The project targets **practical Redis-compatible application workloads**, while
remaining explicit about features or edge cases that are not yet exact Redis
parity.

SnugKV intentionally exposes **one logical database**.

The current distributed implementation has completed the planned
production-hardening matrix and the post-merge project-wide release validation for
the audited production-candidate scope. The project should still be described as
**alpha / production-candidate work**, not as a mature drop-in replacement with
exhaustive Redis parity or years of production history.

---

## 2. Mental model

Think of SnugKV as four systems living together:

1. **Redis-compatible server**
   - RESP2/RESP3 wire protocol
   - common Redis commands and tooling
   - Redis-shaped errors and reply forms where audited

2. **Memory-efficient storage engine**
   - sharded in-memory engine
   - compact index/entry layout
   - packed native containers
   - adaptive encoding/compression
   - explicit engine memory accounting

3. **Durable datastore**
   - logical AOF
   - snapshots
   - restart recovery
   - persistence-aware transactions/scripts/functions
   - replication state persistence where supported

4. **Distributed datastore**
   - primary/replica replication
   - partial resynchronization
   - automatic failover
   - quorum leases / write fencing
   - 16,384 Redis hash slots
   - MOVED / ASK routing
   - resharding / recovery
   - explicit cluster membership
   - replica-aware topology
   - operator health/consistency views

---

## 3. Technology and architecture

- Language: **Go**
- Current documented requirement: **Go 1.27+**
- Client protocol: **RESP2 + RESP3**
- Default application model: Redis-compatible TCP clients
- Data model: one logical database
- Concurrency model: sharded storage plus command-specific coordination
- Persistence: logical AOF and snapshots
- Distributed model: replication + failover groups + Redis-style Cluster slot routing
- Observability: INFO, CLUSTER diagnostics, SNUG.* diagnostics, optional Prometheus metrics
- Security: AUTH/ACL, replication auth/TLS, dedicated internal cluster-control authentication

Important design principle:

> Prefer explicit, testable semantics over pretending to implement Redis behavior
> that SnugKV cannot yet guarantee.

---

## 4. Capability matrix

### Core key/value functionality — broad support

Implemented families include:

- STRING-style values
- numeric operations
- bit operations
- expiration / TTL
- keyspace operations
- HASH
- SET
- LIST
- ZSET
- STREAM
- HyperLogLog
- GEO
- SORT / SORT_RO
- COPY
- blocking list/zset/stream reads
- SCAN-family iteration

Representative commands include SET/GET/MGET/MSET, INCR*, EXPIRE*, HSET/HGET,
SADD/SMEMBERS, LPUSH/LRANGE, ZADD/ZRANGE, XADD/XREAD/XREADGROUP, PFADD,
GEOSEARCH, SORT, COPY and many related commands.

See `README.md` and `COMPATIBILITY.md` for the exact command surface.

### Transactions and concurrency — broad support

Implemented:

- MULTI
- EXEC
- DISCARD
- WATCH
- UNWATCH
- queue-time errors
- EXEC-time errors
- cross-client WATCH invalidation
- transaction persistence
- ACL re-authorization at EXEC

Blocking operations use waiter/wakeup paths instead of polling.

### RESP3 — broad audited support

Implemented:

- HELLO 3
- HELLO 2 protocol switching
- null/map/set/double/verbatim reply forms needed by the audited surface
- nested COMMAND/ACL shapes
- RESP3 Pub/Sub push messages

Not claimed:

- exhaustive RESP3 type/attribute parity for every possible Redis client behavior

### Pub/Sub — broad support

Implemented:

- classic Pub/Sub
- pattern Pub/Sub
- sharded Pub/Sub
- RESP2 subscription behavior
- RESP3 push behavior
- Pub/Sub introspection

### Authentication and ACL — broad audited support

Implemented:

- AUTH
- named users
- command/category permissions
- key patterns
- channel patterns
- selectors
- ACL LOG
- ACL DRYRUN
- ACL GENPASS
- ACL SAVE / LOAD
- persisted ACL files
- transaction enforcement
- fail-closed malformed configured ACL files

### CLIENT / COMMAND / CONFIG tooling

Broad tooling exists, including:

- CLIENT ID / GETNAME / SETNAME / SETINFO
- CLIENT INFO / LIST / KILL / UNBLOCK
- Redis-shaped COMMAND metadata
- COMMAND DOCS
- COMMAND GETKEYS / GETKEYSANDFLAGS
- CONFIG GET / SET / RESETSTAT / REWRITE / HELP

`CLIENT TRACKING`, `CLIENT CACHING`, `CLIENT GETREDIR`, BCAST/PREFIX,
OPTIN/OPTOUT, NOLOOP, REDIRECT, RESP3 invalidation pushes, and broken-redirect
notification are implemented and audited. Additional CLIENT connection classes
remain a compatibility boundary.

### Lua scripting

Implemented:

- EVAL
- EVALSHA
- EVAL_RO
- EVALSHA_RO
- SCRIPT LOAD
- SCRIPT EXISTS
- SCRIPT FLUSH
- SCRIPT KILL
- redis.call / redis.pcall
- KEYS / ARGV
- Redis-style status/error helper functions

Scripts execute under the datastore command-serialization boundary so other
clients cannot interleave ordinary commands inside one script execution.

### Redis Functions

Implemented broad management/execution support:

- FUNCTION LOAD
- FUNCTION LIST
- FUNCTION DELETE
- FUNCTION FLUSH
- FUNCTION DUMP
- FUNCTION RESTORE
- FUNCTION STATS
- FUNCTION KILL
- FUNCTION HELP
- FCALL
- FCALL_RO

SnugKV Function dump/restore uses Redis 8.2-compatible Function RDB payloads for
the audited surface, including LZF strings plus version/CRC validation and
cross-restore fixtures.

### JSON

SnugKV has a first-class JSON command family.

The JSONPath implementation has been differentially audited against RedisJSON-like
behavior across a broad surface including:

- member/index selectors
- wildcards
- recursive descent
- slices
- unions
- filters
- arithmetic
- regex
- membership/set operators
- length/numeric-style functions
- multi-match mutation/deletion

JSON object insertion-order preservation and exact error text are not always
claimed to match Redis byte-for-byte.

### Search

SnugKV implements a substantial JSON-backed Search surface including:

- FT.CREATE
- FT.SEARCH
- FT.AGGREGATE
- FT.INFO
- FT.DROPINDEX
- FT._LIST

Supported index/query concepts include:

- TEXT
- TAG
- NUMERIC
- GEO
- VECTOR FLAT
- FLOAT32
- COSINE
- KNN
- VECTOR_RANGE
- stemming
- NOSTEM
- stopwords
- language selection
- exact phrases
- fuzzy terms
- wildcard/prefix/suffix/contains matching
- phonetic matching
- BM25STD-style scoring
- field weights
- SORTBY
- LIMIT
- aggregate LOAD/FILTER/GROUPBY/reducers

Search compatibility is intentionally audited in slices rather than advertised as
complete Redis Search parity.

---

## 5. Native storage and memory strategy

SnugKV does not simply represent all Redis datatypes as generic blobs.

Native semantic storage includes compact physical encodings for:

- HASH
- SET
- LIST
- ZSET
- STREAM

Examples of storage strategies used by the project:

- compact open-addressed index slots;
- compact stored-entry records;
- lazy metadata sidecars;
- inline tiny scalars where possible;
- shared HASH field-shape encoding;
- prefix coding;
- score delta encoding;
- compact container-specific formats;
- background/adaptive optimization;
- explicit compaction;
- engine-accounted memory limits.

SnugKV's memory figures are **engine-accounted memory**, not operating-system RSS.
Go runtime state, network buffers, stacks, persistence buffers and temporary
optimizer allocations can make RSS higher.

---

## 6. Persistence and durability

Implemented durability features include:

- logical AOF
- snapshots
- startup snapshot load + AOF replay
- fsync modes:
  - always
  - everysec
  - no
- checksum validation
- truncated-final-frame handling
- append failure rollback/fencing behavior
- AOF rewrite/export paths
- persistence-aware transaction changes
- script/function logical persistence
- restart recovery for durable cluster transition state where supported

Persistence code is treated as correctness-critical. Changes must preserve:

- acknowledged-write durability semantics;
- partial-failure accounting;
- restart recovery;
- no silent ownership finalization after failed migration;
- no silent mutation loss after AOF failure.

---

## 7. Replication

The replication implementation includes:

- primary/replica topology
- full sync
- live replication
- bounded backlog
- Redis next-byte PSYNC offsets
- partial resynchronization
- fallback full resync
- periodic replica ACKs
- replica lag/offset observability
- Redis RDB full-sync interoperability across the audited encoding surface
- diskless EOF framing
- replication authentication
- replication TLS
- optional mTLS
- graceful-restart PSYNC continuation

Redis -> SnugKV live interoperability has been used as an oracle for multiple
replication/RDB slices.

Crash-resume behavior that requires atomic persistence of every upstream offset
with every replicated mutation should only be claimed where the relevant audit
explicitly says it is supported.

---

## 8. Automatic failover

Implemented failover features include:

- failure detection
- majority election
- durable vote state
- deterministic candidate ranking
- leader promotion
- quorum-backed leader leases
- lease renewal
- write fencing after quorum loss
- foreign-leader lease fencing
- surviving-replica reparenting
- returning-primary demotion/reconciliation
- dynamic failover membership
- joint-majority reconfiguration
- authenticated peer discovery
- operator health/topology views
- recovery controls

SnugKV does **not** claim to be a general-purpose consensus database. The quorum
lease/election mechanisms are scoped to the implemented failover model.

---

## 9. Redis Cluster-style sharding

Implemented distributed Cluster functionality includes:

- Redis CRC16/hash-tag rules
- 16,384 hash slots
- MOVED
- ASK
- ASKING
- CROSSSLOT
- CLUSTERDOWN for unserved slots
- CLUSTER SLOTS
- CLUSTER NODES
- CLUSTER SHARDS
- CLUSTER INFO
- CLUSTER HEALTH
- CLUSTER CONSISTENCY
- guarded SETSLOT transitions
- GETKEYSINSLOT / COUNTKEYSINSLOT
- cluster-aware MIGRATE / RESTORE-ASKING
- deterministic rebalance planning
- DRYRUN
- bounded/batched moves
- multi-donor coordination
- rebalance STATUS
- explicit RECOVER PLAN / RESUME
- persisted MIGRATING/IMPORTING state
- restart-safe interrupted migration recovery
- stale-coordinator fencing
- topology ownership-digest checks
- failover/rebalance serialization
- explicit cluster membership
- safe evacuation/removal
- replica-aware shard topology

Automatic membership admission remains optional/deferred.

---

## 10. Internal cluster security

Configured clusters require a dedicated:

`cluster_control_auth`

Internal peers establish a connection-scoped:

`SNUG.INTERNAL AUTH <secret>`

Private cluster/failover RPCs require that internal identity.

Important security properties:

- ordinary highly privileged Redis ACL clients do not automatically gain internal-control authority;
- wrong internal secrets fail closed;
- AUTH / HELLO / RESET revoke the internal identity;
- operator-facing diagnostics remain on the ordinary ACL surface;
- internal rebalance migration can use the verified TLS replication dialer;
- public Redis-compatible MIGRATE retains its ordinary transport semantics.

---

## 11. Distributed validation evidence

The distributed system is not considered complete merely because unit tests pass.
It has dedicated multi-process and failure-injection validation.

Current retained evidence includes:

### Recovery / migration

- source crash during migration
- target crash during migration
- restart recovery
- lost/reasserted target IMPORTING state
- repeated interrupted recovery
- persistence/disk failure injection
- corrupted-replica fail-closed/rebuild recovery

### Important migration bug that was found and fixed

An extended chaos soak reached **70 consecutive passing cases** before exposing a
real MIGRATE timeout problem.

The old implementation set one read deadline for an entire pipelined migration
batch. With up to 64 RESTORE operations and `fsync=always`, continuous progress
could still exceed that absolute deadline.

The fix refreshes the read deadline before every expected reply, so timeout means
**lack of progress**, not "the whole batch must finish inside one timeout window."

Regression coverage also verifies:

- acknowledged keys remain deleted from source after a later timeout;
- acknowledged deletion is persisted;
- unacknowledged source keys remain;
- recovery retries transport failures only on the REPLACE-based recovery path;
- retry accounting does not double-count moved keys.

### Focused stress results

- repeated crash/recovery: **5/5 passing runs**
- each focused repeated-recovery run used **2,000 durable keys**
- failover-restart stress after harness stabilization fix: **10/10 passing runs**
- those failover runs included an alternate elected leader, not only one fixed path

### Bounded distributed soak

Latest retained bounded validation:

- **6/6 cycles**
- **42/42 cases**
- **0 failures**
- **0 timeouts**
- **952 seconds elapsed (~15m52s)**

The run was configured with `DURATION_SECONDS=1800` and `MAX_CYCLES=6`; it
ended because the six-cycle cap was reached, not because the 30-minute duration
expired.

### Client Cluster smoke

Cluster-mode validation exists for:

- ioredis
- node-redis
- redis-py
- go-redis
- redis-cli routing

### Distributed benchmark coverage

Reproducible benchmark surfaces include:

- direct routing
- redirected routing
- routing cache behavior
- reshard throughput
- TLS migration overhead
- failover recovery timing
- topology/health observation cost

---

## 12. Performance and memory benchmark snapshot

These are development-machine measurements, not universal marketing claims.

Always preserve workload, CPU, pipeline, worker and value-shape context when
quoting them.

### 1M-key random 256-byte SET workload

Recorded comparison on the same 4-logical-CPU development machine:

- SnugKV: about **315k SET/s median**
- Redis 8.2 reference: about **318k SET/s**
- SnugKV engine-accounted memory delta: **~362.27 B/key**
- Redis measured memory delta: **~392.39 B/key**

Interpretation:

- throughput was approximately Redis-class on this specific pipelined workload;
- SnugKV used less measured/accounted memory per key on that same workload;
- this is not a blanket claim that SnugKV is always faster or smaller.

### 1M-key canonical 10-byte counter workload

Latest documented optimized-layout snapshot:

- SnugKV: **77.13 B/key** after load/convergence
- SnugKV: **72.61 B/key** after explicit entry compaction
- Redis reference: **72.39 B/key**
- SnugKV SET: about **402k ops/s**
- SnugKV GET: about **736k ops/s**

This shows the optimized scalar layout approaching Redis memory density for that
specific workload.

### Native container memory results

Recorded 100k-key datatype benchmarks with 16-byte elements/members showed
SnugKV engine-accounted memory below Redis's measured used-memory delta by
approximately:

#### SET

- 8 members: **15.8% lower**
- 16 members: **34.2% lower**
- 32 members: **44.8% lower**
- 64 members: **52.1% lower**

#### LIST

- 16 elements: approximately tied
- 32 elements: **3.2% lower**
- 64 elements: **7.0% lower**

#### ZSET

- 8 members: **17.8% lower**
- 16 members: **34.1% lower**
- 32 members: **45.5% lower**
- 64 members: **51.2% lower**

#### HASH shared-schema workloads

- 8 fields: **13.2% lower**
- 16 fields: **22.8% lower**
- 32 fields: **21.8% lower**
- 64 fields: **22.1% lower**

Tiny containers can still lose to Redis because fixed per-key/index/arena overhead
can dominate at very small cardinalities.

For benchmark methodology and newer measurements, always prefer
`benchmarks/README.md` and committed benchmark result files over this summary.

---

## 13. Testing philosophy

SnugKV relies heavily on differential and failure-oriented testing.

Important validation styles used in the repository:

- unit tests
- race-enabled tests
- RESP fuzzing
- Redis live differential tests
- restart/persistence tests
- protocol-fragmentation tests
- ACL differential tests
- Lua/Function differential tests
- JSONPath differential tests
- Search differential tests
- Redis RDB interoperability tests
- multi-process replication tests
- Cluster client-library tests
- chaos/failure injection
- partition/heal scenarios
- kill/restart scenarios
- persistence failure scenarios
- bounded soak tests
- reproducible benchmarks

Baseline release checks for distributed-system changes include:

```sh
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
```

These project-wide checks were operator-reported green on `main` after PR #249,
closing the distributed production-candidate milestone. RESP fuzz and
branch-specific compatibility checks should continue to remain green for future
changes.

---

## 14. Compatibility philosophy

Do not equate "command exists" with "perfect Redis replacement."

SnugKV uses several levels of confidence:

- implemented
- differentially audited
- live Redis validated
- tested under restart/failure
- benchmarked
- intentionally deferred / known boundary

When modifying a feature, determine which of those levels the feature currently
has and preserve or improve the evidence.

Do not silently upgrade a compatibility claim from "implemented" to "Redis parity"
without a Redis oracle/differential test supporting that claim.

---

## 15. Important current boundaries

Examples of boundaries that agents should understand:

- project is still alpha-stage;
- exhaustive Redis command parity is not claimed;
- exhaustive Redis Cluster parity is not claimed;
- one logical database only;
- fully automatic cluster membership admission is optional/deferred;
- additional Redis CLIENT connection classes remain outside the audited surface;
- RESP3 attributes/exotic client-specific behavior are not fully claimed;
- Lua runs in an embedded Lua 5.1-compatible runtime, not Redis's exact VM;
- Function DUMP/RESTORE is Redis 8.2-compatible for the audited Function RDB
  payload surface, but exhaustive historical/pre-GA payload compatibility is not
  claimed;
- some exact Redis internal representations/diagnostics are intentionally not cloned;
- RedisJSON/Search parity is broad but not exhaustive;
- benchmark results are workload/machine specific;
- long-term large-scale production history does not yet exist.

Check `KNOWN-LIMITATIONS.md` before making a broad product claim.

---

## 16. Documentation authority

When project documents disagree, use this order:

1. current implementation + tests
2. `docs/PROJECT-STATE.md`
3. `PLAN.md`
4. `PROGRESS.md`
5. `COMPATIBILITY.md`
6. `KNOWN-LIMITATIONS.md`
7. focused audit documents
8. older TODO/spec/planning material

Historical audit documents may describe something as "remaining" even when a
later branch completed it.

Never infer current project status from one old unchecked TODO without checking
the newer state and tests.

---

## 17. Files an AI agent should read next

For most tasks, read only what is necessary.

### General project work

1. `AI_AGENT_CONTEXT.md`
2. `docs/PROJECT-STATE.md`
3. `README.md`
4. the relevant source/tests

### Redis compatibility work

1. `COMPATIBILITY.md`
2. `KNOWN-LIMITATIONS.md`
3. the relevant focused audit in `docs/`
4. the related `compat/` harness
5. source + tests

### Distributed work

1. `docs/PROJECT-STATE.md`
2. `docs/CLUSTER-PRODUCTION-HARDENING.md`
3. `docs/AUTOMATIC-FAILOVER-AUDIT.md`
4. `docs/REPLICATION-TLS-AUDIT.md`
5. relevant `scripts/cluster-*.sh`
6. relevant server tests

### Performance/memory work

1. `benchmarks/README.md`
2. committed benchmark result files
3. `cmd/rediswirebench`
4. `cmd/snugbench`
5. profiling docs/results
6. engine/index/arena implementation

---

## 18. Rules for coding agents

### Do

- inspect existing tests before changing semantics;
- use Redis as a live oracle when exact compatibility matters;
- add deterministic regression tests for every bug found by chaos/soak testing;
- preserve persistence/restart behavior;
- preserve ACL metadata and command key extraction;
- consider RESP2 and RESP3 reply differences;
- consider transactions/scripts/functions when changing write semantics;
- consider cluster slot routing for multi-key commands;
- consider replication/AOF effects for durable writes;
- use race tests for concurrent paths;
- measure memory/performance before claiming an optimization;
- update PROJECT-STATE / compatibility docs when a milestone genuinely changes.

### Do not

- rewrite working subsystems just because a simpler implementation looks cleaner;
- assume Redis behavior from memory when a differential test can answer it;
- treat process RSS and SnugKV engine-accounted memory as the same metric;
- retry ambiguous ordinary MIGRATE operations blindly;
- finalize slot ownership while source keys remain;
- bypass internal cluster-control authentication for convenience;
- weaken failure behavior to make a chaos test pass;
- hide a compatibility difference instead of documenting it;
- publish one-off benchmark numbers as universal performance claims.

---

## 19. Useful project commands

Typical development checks:

```sh
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
```

Typical server start:

```sh
go run -buildvcs=false ./cmd/snugkv \
  -listen 127.0.0.1:6380 \
  -admin-listen 127.0.0.1:6381 \
  -metrics-listen 127.0.0.1:9090
```

Typical smoke interaction:

```sh
redis-cli -p 6380 PING
redis-cli -p 6380 SET example hello
redis-cli -p 6380 GET example
```

Docker Compose is also supported for local startup.

---

## 20. Project status in one paragraph

**First-release feature freeze is active.** New functionality should not be added
unless it fixes a release-blocking compatibility defect; current work is
validation, durability/failure injection, soak, profiling, packaging, security
review, quickstart, and release-candidate preparation.

SnugKV is an ambitious Go-based Redis-compatible datastore with a broad
single-node command surface, compact native container storage, JSON/Search,
scripting/functions, persistence, ACL/security, replication, quorum-backed
automatic failover and Redis-style Cluster routing/resharding. The project has
moved well beyond a prototype: many surfaces have live Redis differential audits,
the distributed system has multi-process chaos/recovery tests, four major Cluster
client-library smokes plus persistent failover recovery for all four clients, a
bounded three-node bootstrap helper, reproducible distributed benchmarks, 5/5
repeated-recovery stress runs, 10/10 failover-restart stress runs and a 42/42
bounded distributed soak. It is still alpha-stage and should not be represented as exhaustive Redis
parity or as having mature long-term production history. The main engineering
principle is to make compatibility and durability claims only where the
implementation, tests and retained evidence justify them.
