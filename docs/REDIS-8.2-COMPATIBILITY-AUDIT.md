# Redis 8.2 Compatibility Audit and Completion Plan

Status: implementation-planning audit  
Target: Redis 8.2 command surface  
Source of Redis truth: https://redis.io/docs/latest/commands/redis-8-2-commands/  
SnugKV source of truth: current `main`, `PLAN.md`, implementation code, merged compatibility audits, and issue #55.

## Goal

Finish Redis 8.2-compatible functionality before returning to broad differential
testing, performance tuning, memory optimization, or benchmark-driven work.

The order for this roadmap is therefore:

1. identify missing/partial Redis 8.2 functionality;
2. implement the missing functionality;
3. close intentional/documented compatibility boundaries;
4. only then run the exhaustive Redis 8.2 differential suite;
5. only after correctness is stable, resume efficiency/performance work.

This audit deliberately distinguishes:

- **Supported** — implemented and already documented/audited strongly enough to
  treat as present.
- **Partial** — substantial implementation exists but Redis 8.2 exposes a wider
  command or option surface.
- **Missing** — no current implementation evidence was found.
- **Intentional boundary** — incompatible by architecture/product decision, not
  an accidental omission.
- **Distributed later** — requires Cluster/failover architecture rather than a
  standalone command addition.

## Audit scope rule

The canonical command target is the functional command sections on Redis's
version-specific Redis 8.2 command reference. The global Redis documentation
sidebar also contains product-specific, internal, and commands from other
surfaces, so the sidebar alone is not used as the compatibility inventory.

Redis 8.2 functional sections are:

- Strings
- Hashes
- Lists
- Sets
- Sorted sets
- Streams
- Bitmaps
- HyperLogLog
- Geospatial
- JSON
- Search
- Time series
- Vector sets
- Pub/Sub
- Transactions
- Scripting / Functions
- Connection
- Server
- Cluster
- Generic

Redis 8 also integrates formerly module-provided data structures such as Bloom,
Cuckoo, CMS, TopK, and t-digest; those are tracked separately below because
SnugKV already implements substantial portions of them.

---

# 1. Standalone core data commands

## Strings

### Supported

SnugKV already covers the ordinary Redis string/counter/range surface including:

`APPEND`, `DECR`, `DECRBY`, `GET`, `GETDEL`, `GETEX`,
`GETRANGE`, `GETSET`, `INCR`, `INCRBY`, `INCRBYFLOAT`,
`MGET`, `MSET`, `MSETNX`, `PSETEX`, `SET`, `SETEX`,
`SETNX`, `SETRANGE`, `STRLEN`, and the legacy substring behavior.

### Missing

- [ ] `LCS`

Priority: **P0-small**. This is a self-contained standalone command and should be
completed before larger module families.

---

## Hashes

### Supported

SnugKV has broad native HASH support plus Redis hash-field-expiration primitives,
including the 7.4 field-TTL command family and Redis-compatible HFE persistence
work.

### Missing / no current implementation evidence

Redis 8.2 includes the Redis 8 hash convenience commands:

- [ ] `HGETDEL`
- [ ] `HGETEX`
- [ ] `HSETEX`

Before implementation, verify that none is registered indirectly under a newer
file. They are not part of the currently advertised HASH command list and no
dedicated implementation commit was found during this audit.

Priority: **P0-small**. These should reuse the existing hash field-expiry
machinery rather than introduce new storage concepts.

### Verify during implementation

- [ ] `HSCAN ... NOVALUES` option parity

---

## Lists

### Supported

Broad LIST support exists, including push/pop, ranges, insert/position, moves,
and the classic blocking list operations.

### Missing

- [ ] `LMPOP`
- [ ] `BLMPOP`

Priority: **P0-small/medium**. SnugKV already has ZMPOP/BZMPOP-style parsing and
blocking waiter infrastructure, so these should be implemented before larger
features.

---

## Sets

### Supported

Broad SET algebra, membership, random/pop, move, scan, and STORE variants exist.

### Missing

- [ ] `SINTERCARD`

Priority: **P0-small**.

---

## Sorted sets

Status: **Supported / broad**.

The Redis 8.2 sorted-set family is already covered at a high level, including
ZMPOP/BZMPOP, ZINTERCARD, algebra/store variants, range modes, blocking pops,
random-member and scan functionality.

Action:
- [ ] no new family implementation planned until final differential audit finds a
  concrete omission.

---

## Streams

Status: **Supported / strong Redis 8.2 coverage**.

Implemented work includes consumer groups, claims, PEL, trimming, XINFO,
Redis 8.2 `KEEPREF` / `DELREF` / `ACKED`, `XDELEX`, and
`XACKDEL`.

Known intentional representation differences:
- approximate `XTRIM ~` deletion granularity can differ;
- Redis radix-tree diagnostic counts are not fabricated.

Low-priority/internal check:
- [ ] confirm whether standalone exposure of internal `XSETID` is required for
  the chosen compatibility claim. Replication semantics matter more than
  user-facing support for this internal command.

---

## Bitmaps

### Supported

- `GETBIT`
- `SETBIT`
- `BITCOUNT`
- `BITPOS`
- `BITOP`

SnugKV already accepts the Redis 8.2 BITOP operators:
`AND`, `OR`, `XOR`, `NOT`, `DIFF`, `DIFF1`, `ANDOR`, and
`ONE`.

### Missing

- [ ] `BITFIELD`
- [ ] `BITFIELD_RO`

Priority: **P0-medium**.

---

## HyperLogLog

Status: **Supported for application surface**.

- `PFADD`
- `PFCOUNT`
- `PFMERGE`

Redis also exposes internal/debug commands:
- `PFDEBUG`
- `PFSELFTEST`

Decision:
- [ ] keep these as **low-priority internal/debug compatibility**, not P0
  application compatibility.

---

## Geospatial

Status: **Broad support**.

Modern GEO is implemented. Legacy radius aliases have also been implemented and
audited in newer work.

Action:
- [ ] verify `GEORADIUS_RO` and `GEORADIUSBYMEMBER_RO` registration explicitly
  during the final command inventory pass.

---

# 2. JSON

SnugKV now has broad first-class JSON and substantial JSONPath support.

### Implemented command surface

Current registered commands include:

`JSON.SET`, `JSON.GET`, `JSON.TYPE`, `JSON.DEL`,
`JSON.NUMINCRBY`, `JSON.STRLEN`, `JSON.ARRLEN`, `JSON.OBJLEN`,
`JSON.ARRAPPEND`, `JSON.STRAPPEND`, `JSON.OBJKEYS`,
`JSON.TOGGLE`, `JSON.ARRPOP`, `JSON.ARRINSERT`, `JSON.ARRINDEX`,
`JSON.CLEAR`, `JSON.ARRTRIM`, `JSON.MGET`, `JSON.MERGE`,
`JSON.MSET`, and `JSON.FORGET`.

### Missing Redis 8.2 commands

- [ ] `JSON.NUMMULTBY`
- [ ] `JSON.RESP`
- [ ] `JSON.DEBUG`
- [ ] `JSON.DEBUG MEMORY`

Priority:
1. **P1** `JSON.NUMMULTBY`
2. **P1** `JSON.RESP`
3. **P2 tooling** `JSON.DEBUG MEMORY`
4. **P2 tooling** `JSON.DEBUG` container semantics

Known compatibility boundary to retain/document:
- Go-map object decoding can change JSON object member insertion order;
- exact JSONPath error wording is not always byte-identical.

---

# 3. Redis Search / Query Engine

Status: **Partial, substantial implementation**.

SnugKV already has:

- `FT.CREATE`
- `FT.SEARCH`
- `FT.AGGREGATE`
- `FT.INFO`
- `FT.DROPINDEX`
- `FT._LIST`

and significant TAG/NUMERIC/TEXT/GEO/VECTOR query behavior.

Redis 8.2 exposes a much wider Search management/tooling surface.

### Missing command families

#### Alias management
- [ ] `FT.ALIASADD`
- [ ] `FT.ALIASDEL`
- [ ] `FT.ALIASLIST`
- [ ] `FT.ALIASUPDATE`

#### Schema/index mutation
- [ ] `FT.ALTER`

#### Search configuration
- [ ] `FT.CONFIG GET`
- [ ] `FT.CONFIG SET`

#### Aggregate cursors
- [ ] `FT.CURSOR READ`
- [ ] `FT.CURSOR DEL`

#### Dictionaries
- [ ] `FT.DICTADD`
- [ ] `FT.DICTDEL`
- [ ] `FT.DICTDUMP`

#### Explain/profile
- [ ] `FT.EXPLAIN`
- [ ] `FT.EXPLAINCLI`
- [ ] `FT.PROFILE`

#### Spellcheck / synonyms / tags
- [ ] `FT.SPELLCHECK`
- [ ] `FT.SYNDUMP`
- [ ] `FT.SYNUPDATE`
- [ ] `FT.TAGVALS`

#### Suggestions
- [ ] `FT.SUGADD`
- [ ] `FT.SUGDEL`
- [ ] `FT.SUGGET`
- [ ] `FT.SUGLEN`

#### Hybrid query
- [ ] `FT.HYBRID`

Priority: **P2-major**.

Implementation order inside Search:
1. alias management;
2. ALTER;
3. cursor support;
4. explain/profile;
5. TAGVALS;
6. synonyms/dictionaries/spellcheck;
7. suggestions;
8. HYBRID and remaining advanced query options.

Reason: aliases/ALTER/cursors/tooling unlock common client compatibility before
the larger NLP/query-expansion features.

---

# 4. TimeSeries

Status: **Partial**.

### Implemented

- `TS.CREATE`
- `TS.ADD`
- `TS.GET`
- `TS.RANGE`
- `TS.REVRANGE`
- `TS.INCRBY`
- `TS.DECRBY`
- `TS.DEL`
- `TS.INFO`

### Missing Redis 8.2 command surface

- [ ] `TS.ALTER`
- [ ] `TS.CREATERULE`
- [ ] `TS.DELETERULE`
- [ ] `TS.MADD`
- [ ] `TS.MGET`
- [ ] `TS.MRANGE`
- [ ] `TS.MREVRANGE`
- [ ] `TS.QUERYINDEX`

Priority: **P1-major**.

Implementation sequence:
1. `TS.ALTER`
2. `TS.MADD`
3. label index + `TS.QUERYINDEX`
4. `TS.MGET`
5. `TS.MRANGE` / `TS.MREVRANGE`
6. compaction rules: `TS.CREATERULE` / `TS.DELETERULE`

This ordering builds the label/index substrate before multi-series range queries
and builds aggregation/rule machinery last.

---

# 5. Vector Sets

Status: **Missing family**.

Redis 8.2 exposes Vector Sets as a first-class command family. SnugKV has vector
search support inside FT indexes, but that is not the same Redis data type or wire
surface.

### Missing

- [ ] `VADD`
- [ ] `VCARD`
- [ ] `VDIM`
- [ ] `VEMB`
- [ ] `VGETATTR`
- [ ] `VINFO`
- [ ] `VISMEMBER`
- [ ] `VLINKS`
- [ ] `VRANDMEMBER`
- [ ] `VREM`
- [ ] `VSETATTR`
- [ ] `VSIM`

Priority: **P1-major and high product value**.

Recommended implementation sequence:
1. native Vector Set datatype + persistence;
2. `VADD`, `VREM`, `VCARD`, `VISMEMBER`;
3. dimension/embedding inspection: `VDIM`, `VEMB`;
4. attributes: `VSETATTR`, `VGETATTR`;
5. `VRANDMEMBER`, `VINFO`;
6. similarity query `VSIM`;
7. graph/link introspection `VLINKS`.

Do not reuse the Search index as the semantic storage layer unless Redis-visible
Vector Set mutation, persistence, type, and command behavior can still be matched.

---

# 6. Probabilistic data structures integrated into Redis 8

## Bloom

Implemented:
`BF.RESERVE`, `BF.ADD`, `BF.EXISTS`, `BF.MADD`, `BF.MEXISTS`,
`BF.CARD`, `BF.INFO`, `BF.INSERT`.

Missing:
- [ ] `BF.SCANDUMP`
- [ ] `BF.LOADCHUNK`

Priority: **P1-small**.

## Cuckoo

Implemented:
`CF.RESERVE`, `CF.ADD`, `CF.ADDNX`, `CF.EXISTS`,
`CF.MEXISTS`, `CF.COUNT`, `CF.DEL`, `CF.INSERT`,
`CF.INSERTNX`, `CF.INFO`.

Missing:
- [ ] `CF.SCANDUMP`
- [ ] `CF.LOADCHUNK`

Priority: **P1-small**.

## Count-Min Sketch

Status: **broad/complete command family**:
`CMS.INITBYDIM`, `CMS.INITBYPROB`, `CMS.INCRBY`, `CMS.QUERY`,
`CMS.MERGE`, `CMS.INFO`.

## TopK

Status: **broad/complete command family**:
`TOPK.RESERVE`, `TOPK.ADD`, `TOPK.INCRBY`, `TOPK.QUERY`,
`TOPK.COUNT`, `TOPK.LIST`, `TOPK.INFO`.

## t-digest

Status: **broad command family**:
`TDIGEST.CREATE`, `ADD`, `MERGE`, `RESET`, `MIN`, `MAX`,
`QUANTILE`, `CDF`, `RANK`, `REVRANK`, `BYRANK`,
`BYREVRANK`, `TRIMMED_MEAN`, `INFO`.

---

# 7. Pub/Sub and Transactions

Status: **Broad support**.

No new implementation phase is currently planned before the final Redis
differential sweep.

---

# 8. Scripting and Functions

Status: **Broad/advanced support**.

Current newer project state includes:
- EVAL/EVALSHA and read-only variants;
- SCRIPT cache management and KILL;
- Redis Functions management and FCALL/FCALL_RO;
- FUNCTION DUMP/RESTORE work;
- allow-oom and command/OOM parity audits;
- SCRIPT DEBUG/LDB work beyond the older compatibility document.

Action:
- [ ] reconcile `PLAN.md`, issue #55, and `COMPATIBILITY.md` so they no longer
  contradict one another about SCRIPT DEBUG and Function payload compatibility.
- [ ] final command-level audit only after missing functionality phases are done.

No new scripting implementation is P0 based on the newest project state.

---

# 9. Connection / CLIENT

Redis 8.2's connection section is broader than SnugKV's original CLIENT slice.

### Supported

- AUTH
- CLIENT CACHING
- CLIENT GETNAME
- CLIENT GETREDIR
- CLIENT ID
- CLIENT INFO
- CLIENT LIST (core forms)
- CLIENT KILL (core ID form)
- CLIENT SETINFO
- CLIENT SETNAME
- CLIENT TRACKING
- CLIENT UNBLOCK
- ECHO
- HELLO
- PING
- QUIT
- RESET
- SELECT 0

Client tracking, redirect, BCAST/PREFIX, OPTIN/OPTOUT, NOLOOP and RESP3
invalidation pushes were implemented in later work even though
`COMPATIBILITY.md` still contains stale text.

### Missing / partial

- [ ] `CLIENT TRACKINGINFO`
- [ ] `CLIENT NO-EVICT`
- [ ] `CLIENT NO-TOUCH`
- [ ] `CLIENT PAUSE`
- [ ] `CLIENT UNPAUSE`
- [ ] `CLIENT REPLY`
- [ ] expand `CLIENT KILL` filters beyond the current core ID/SKIPME form where
  meaningful
- [ ] support additional `CLIENT LIST TYPE` classes when corresponding
  connection classes exist

Priority: **P1-medium**.

Notes:
- NO-TOUCH interacts with SnugKV's activity/LRU tracking and must be implemented
  semantically, not as a no-op.
- NO-EVICT matters only if SnugKV implements an equivalent client-output-buffer
  eviction policy. If not, classify it explicitly rather than pretending.
- SELECT for non-zero databases remains an intentional boundary.

---

# 10. Server / operational commands

This is one of the largest remaining standalone compatibility areas, but not all
commands have equal application value.

## Already broad

- ACL family
- COMMAND metadata core
- CONFIG supported settings
- DBSIZE
- FLUSHDB / FLUSHALL
- INFO
- MEMORY USAGE
- PSYNC / REPLICAOF / ROLE
- WAIT / WAITAOF
- replication topology functionality

## Missing high-value tooling

- [ ] `COMMAND LIST`
- [ ] `TIME`
- [ ] `LASTSAVE`
- [ ] `SLOWLOG GET`
- [ ] `SLOWLOG LEN`
- [ ] `SLOWLOG RESET`
- [ ] `MONITOR`

Priority: **P1/P2 tooling**.

## Persistence/admin commands to decide and implement semantically

- [ ] `SAVE`
- [ ] `BGSAVE`
- [ ] `BGREWRITEAOF`
- [ ] `SHUTDOWN`

These should map to SnugKV's persistence model rather than return Redis-looking
responses with no corresponding operation.

Priority: **P2**.

## Replication/failover

- [ ] `FAILOVER`

Priority: **P3 distributed** because coordinated promotion depends on topology
semantics, not just parsing a command.

## Latency observability

- [ ] `LATENCY DOCTOR`
- [ ] `LATENCY GRAPH`
- [ ] `LATENCY HISTOGRAM`
- [ ] `LATENCY HISTORY`
- [ ] `LATENCY LATEST`
- [ ] `LATENCY RESET`

Priority: **P3 tooling** after functionality.

## MEMORY subcommands

Currently `MEMORY USAGE` exists.

Missing or intentionally not equivalent:
- [ ] `MEMORY STATS`
- [ ] `MEMORY DOCTOR`
- [ ] `MEMORY PURGE`
- [ ] `MEMORY MALLOC-STATS`

Because SnugKV uses Go runtime allocation rather than Redis's allocator model,
`MALLOC-STATS` cannot be cloned literally. Decide whether to provide a
Redis-compatible command with SnugKV-specific meaningful content or document it
as an intentional runtime boundary.

## MODULE

Redis exposes `MODULE LIST/LOAD/LOADEX/UNLOAD`.

Recommendation: **intentional boundary** unless SnugKV decides to build a Redis
module ABI. Redis 8 data structures that SnugKV implements natively do not require
pretending to support Redis's module loader.

---

# 11. Generic / keyspace commands

Core TTL, RENAME, COPY, DUMP/RESTORE, MIGRATE, SCAN, SORT, TYPE, UNLINK, etc. are
already broad.

### Missing

- [ ] `OBJECT ENCODING`
- [ ] `OBJECT FREQ`
- [ ] `OBJECT IDLETIME`
- [ ] `OBJECT REFCOUNT`

Priority: **P2 tooling**.

Semantics must reflect SnugKV's real representation. For example, REFCOUNT and
ENCODING should not claim Redis SDS/listpack encodings that do not exist.

### Intentional single-database boundaries

- `MOVE`
- `SWAPDB`
- non-zero `SELECT`
- cross-database COPY

These should remain explicit incompatibilities unless SnugKV chooses to introduce
multiple logical databases.

---

# 12. RESP3

Status: **Broad supported surface**.

Remaining optional gap:
- RESP3 attribute frames and otherwise-unused protocol types.

Priority: **P3**, only when required by a newly implemented Redis 8.2 command.

---

# 13. Persistence and replication compatibility

Replication has advanced substantially beyond the older public compatibility
text:

- primary/replica topology;
- full logical sync;
- PSYNC continuation/backlog;
- ACK/observability;
- Redis RDB full-sync support for audited core encodings;
- diskless/EOF framing;
- authentication;
- TLS;
- restart PSYNC continuation.

### Remaining

- [ ] broader Redis RDB object encodings encountered in real Redis datasets
- [ ] ensure newly added Redis 8 data structures have correct SnugKV
  replication/persistence behavior
- [ ] verify Function payload state against the newest implementation/docs and
  remove stale documentation

Priority: **P2/P3 after standalone command families**.

---

# 14. Cluster

Status: **Missing / distributed later**.

Redis Cluster is not a group of independent commands that can be added one by
one. Proper compatibility requires:

1. Redis CRC16 hash slots and hash tags;
2. slot ownership;
3. CROSSSLOT validation;
4. MOVED redirections;
5. ASK / ASKING redirections;
6. cluster node identity/configuration epochs;
7. cluster gossip/bus or an equivalent topology protocol;
8. slot migration/import;
9. replica relationships per shard;
10. cluster failover;
11. CLUSTER command family;
12. client-visible CLUSTER SLOTS / SHARDS / NODES semantics.

Priority: **P4-largest architectural phase**.

Do not start Cluster until standalone command compatibility is substantially
complete.

---

# 15. Sentinel / automatic failover

Redis Sentinel is operationally separate from the normal data-command surface,
but automatic failover is still a major deployment compatibility gap.

SnugKV already has replication and manual promotion foundations.

Needed later:
- topology monitoring;
- failure detection;
- leader/election or coordinator semantics;
- automatic replica promotion;
- primary reconfiguration;
- client discovery / Sentinel-compatible endpoint only if desired.

Priority: **P4 alongside/after Cluster design**.

---

# 16. Documentation drift discovered by this audit

The repository currently has multiple compatibility documents at different
ages.

Examples:
- `COMPATIBILITY.md` says advanced CLIENT tracking/caching/redirection is
  missing, while later merged work and issue #55 mark it implemented.
- older scripting/Functions statements lag newer PLAN/audit work.
- older distributed-status text understates current replication support.
- older GEO text can understate the legacy alias work.

Action:
- [ ] make this audit + `PLAN.md` the active implementation source of truth;
- [ ] update `COMPATIBILITY.md` after each implementation phase;
- [ ] close stale checkboxes in issue #55 or replace the issue body with links to
  this document.

Priority: **P0 documentation**, but do not let documentation cleanup block feature
implementation.

---

# Implementation priority

## Phase A — finish small standalone core gaps first

These are high compatibility value for relatively contained implementation work.

1. [ ] `LCS`
2. [ ] `HGETDEL`
3. [ ] `HGETEX`
4. [ ] `HSETEX`
5. [ ] `LMPOP`
6. [ ] `BLMPOP`
7. [ ] `SINTERCARD`
8. [ ] `BITFIELD`
9. [ ] `BITFIELD_RO`

Also verify:
- [ ] HSCAN NOVALUES
- [ ] GEO read-only legacy aliases

**Do this phase before any new benchmarking work.**

## Phase B — close compact Redis 8 integrated-data gaps

1. [ ] JSON.NUMMULTBY
2. [ ] JSON.RESP
3. [ ] JSON.DEBUG MEMORY / JSON.DEBUG
4. [ ] BF.SCANDUMP / BF.LOADCHUNK
5. [ ] CF.SCANDUMP / CF.LOADCHUNK
6. [ ] CLIENT TRACKINGINFO
7. [ ] remaining practical CLIENT controls

## Phase C — complete TimeSeries

1. [ ] TS.ALTER
2. [ ] TS.MADD
3. [ ] TS.QUERYINDEX
4. [ ] TS.MGET
5. [ ] TS.MRANGE
6. [ ] TS.MREVRANGE
7. [ ] TS.CREATERULE
8. [ ] TS.DELETERULE

## Phase D — implement Vector Sets

Implement the complete Vector Set family as one coherent feature milestone.

## Phase E — complete Search command breadth

Implement alias/schema/cursor/tooling/dictionary/spellcheck/synonym/suggestion/
hybrid command families.

## Phase F — server/tooling compatibility

Prioritize:
1. COMMAND LIST
2. TIME
3. LASTSAVE
4. SLOWLOG
5. MONITOR
6. persistence admin commands
7. OBJECT
8. LATENCY / MEMORY auxiliary tooling

## Phase G — persistence/replication breadth

Add remaining RDB encodings and newly required cross-compatibility.

## Phase H — Cluster and automatic failover

Only after standalone Redis 8.2 behavior is mature.

---

# Deferred until implementation is complete

Per project priority, do **not** spend the main development cycle on these yet:

- exhaustive command-by-command Redis differential tests;
- large compatibility fuzz matrices;
- benchmark tuning;
- GET/SET hot-path optimization;
- memory-layout optimization;
- geospatial indexing performance;
- Search performance tuning;
- replication throughput optimization.

Basic unit/race tests required to safely implement each feature still remain
mandatory. The deferred item is the *full compatibility/performance campaign*,
not minimum correctness tests for new code.

---

# Definition of implementation-complete

Before entering the final Redis 8.2 differential phase:

1. all Phase A–F commands are either implemented or explicitly classified as an
   intentional boundary;
2. Vector Sets are implemented;
3. TimeSeries command breadth is implemented;
4. Search management/tooling breadth is implemented to the chosen Redis 8.2
   target;
5. single-database exclusions are documented;
6. MODULE ABI and allocator-specific commands are explicitly classified;
7. Cluster/failover is either implemented or clearly separated as the distributed
   compatibility milestone;
8. `COMPATIBILITY.md`, `PLAN.md`, issue #55, and this audit agree.

Then begin the final sequence:

1. frozen Redis 8.2 command inventory;
2. automated SnugKV command inventory;
3. command-by-command differential behavior;
4. RESP2 + RESP3;
5. persistence/restart/replication;
6. client libraries;
7. race/fuzz/soak;
8. only then performance and memory optimization.
