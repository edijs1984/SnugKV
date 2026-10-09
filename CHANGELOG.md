# Changelog

### Feature — atomic transactions on by configuration, atomic scripts and functions

- `-atomic-transactions` (env `SNUGKV_ATOMIC_TRANSACTIONS`, config `atomic_transactions`) makes every `MULTI`/`EXEC` and every writable `EVAL`/`FCALL` all-or-nothing, so existing clients get rollback without sending `MULTI ATOMIC`.
- `#!lua flags=atomic` on a script, or the `atomic` flag on a registered function, rolls back the writes of a script that fails (a Lua error or an error reply). Without the flag the Redis behaviour is unchanged: earlier writes stay.

### Feature — `MULTI ATOMIC`: all-or-nothing transactions

- `MULTI ATOMIC` starts a transaction that is rolled back as a whole if any queued command fails at run time. The keys it can change are snapshotted first and restored exactly (value, type, expiry); transactions with scripts, `FLUSHALL`/`FLUSHDB` or `SORT` snapshot the whole keyspace. Only a committed transaction reaches the append-only file and replicas, and blocked clients are woken after the commit. Commands whose effect cannot be undone (`PUBLISH`, `CONFIG`, `FUNCTION`, ...) are rejected when queued. Plain `MULTI` is unchanged. Tests roll back 45 write commands across every data type and compare the whole database before and after, check the log after a restart, and check that other clients never see rolled-back state. Details: `docs/ATOMIC-TRANSACTIONS.md`.

### Memory — less append headroom on growing sorted sets (pending live benchmark)

- Indexed sorted sets of 64 or more members reserved a full payload of append headroom while they were written; they now reserve half. Right after loading 100,000 members into 100-member sets through the server, memory drops from 122 to 102 bytes/item (1000-member sets: 80 to 64); settled memory is unchanged (38 and 41 bytes/item) and write throughput is within noise. Most of the remaining right-after-load difference is freed blocks of earlier sizes that the maintenance pass reclaims once writes go quiet.

### Throughput — HSET no longer rebuilds the whole hash (pending live benchmark)

- Writing a field to a hash of fewer than 128 fields decoded every field, inserted one and re-encoded the lot, with several allocations per field, all under the shard lock. A profile of 100 connections writing 100-field hashes showed most of the CPU in that rebuild. Hashes of 32 or more fields are now kept in the indexed layout while they are written, so HSET edits the record in place; idle compaction already packs them back down, so settled memory is unchanged (hash with 100 fields: 66 bytes/item settled before and after). The remaining small-hash rebuild no longer copies fields and values it only reads and encodes without sorting a second time.
- Local, 100 connections at pipeline 1, 100 fields per hash, 64-byte values: about 17k -> 32k HSET/s (Redis locally: 45-55k); write p99 21 ms -> 12 ms. At pipeline 256 with 8 workers: 345k -> 416k/s. Memory right after load for 100-field hashes is higher until compaction runs (76 -> 117 bytes/item locally).

### Throughput — index tables grow by half instead of a quarter (pending live benchmark)

- Each time a shard's index outgrew its slots it was rebuilt with room for 25% more keys, and every rebuild rehashes every key. A profile of small JSON values (session-json, 384 B) showed about 27% of load CPU in those rebuilds. Tables now grow with room for 50% more keys, which cuts the total rehash work from about 5x to 3x the key count; Compact still tightens the table to its minimal size once writes stop, so settled memory is unchanged. Local SET load of 1M session-json keys: about 208k -> 238k ops/s (Redis locally: 250-275k). Memory right after load is about 5 bytes/key higher on tiny values (uuid 93 -> 98.5).

### Throughput — optimizer workers trail client writes on a duty cycle (pending live benchmark)

- Workers that got past the foreground-quiet wait (2 s deferral bound or a half-full queue) used to run at the full configured CPU share, 90% on each of the host's cores in dedicated mode, competing with connection handlers during a write burst. While a client write has arrived in the last 20 ms they now run at a 20% duty cycle and return to full speed as soon as writes pause. Local SET load of 1M cache-json / session-json values (1 KiB, 8 workers, pipeline 256): about 105k -> 127k and 107k -> 121k ops/s (Redis locally: 160k and 148k). Settled and post-load memory are identical to before (cache-json 737 MB vs 740 MB total, same convergence time).

### Throughput — incompressible values no longer flood the optimizer (pending live benchmark)

- After 512 consecutive optimizer attempts that produced no rewrite, write-time enqueues are thinned to one in eight until any attempt succeeds, which resets it at once. Recovery sampling and explicit SNUG.COMPACT requests are never thinned. Random 256-byte values spent about a third of load CPU on candidate copies and LZ4 attempts that all ended as raw; local SET load of 1M random keys goes from about 260k to about 340k ops/s (Redis locally: 355-415k). Text, repetitive, counter and uuid are unchanged in throughput and settled memory.

### Memory — idle lists compact into a cold layout (pending live benchmark)

- Idle trim now rewrites an indexed list of 32 or more elements into a cold layout when that lands in a smaller arena block: the records stored back to back with a skip table of one offset per 8 elements instead of one per element, and no append headroom. LINDEX and LRANGE read it directly (one table lookup and at most 7 record skips); LLEN reads the count; every write decodes it and stores the list in the regular layout, so no mutation path knows about it.
- A 1000-element list of 64-byte values is 65,261 bytes cold against 67,015 trimmed. Go rounds blocks above 32 KiB up to whole 8 KiB pages, so that is 8 pages instead of 9. Local run through the server (list-large, 1M items): 73.9 -> 65.7 bytes/item settled (Redis in the lab: 67.8). Right after load is unchanged (85.5). LINDEX throughput on a list that has gone cold is about 8% lower (about 515k against 565k ops/s locally). list-medium is unchanged because its trimmed form already lands in the same block size.

### Memory — idle trim thresholds wait for optimizer rewrites to finish (pending live benchmark)

- The lower idle thresholds for entry and index slack apply only when no optimizer rewrite landed since the previous maintenance step. In the lab run after the previous change, text settled at 182.4 bytes/key (173.0 before) and repetitive took 113 s to converge (60 s before): a compaction during the rewrite phase made in-flight rewrites stale, and on the slower machine they were not redone before the lab's 12 s flat-memory check ended. Counter (59.1) and uuid (84.5) still trim about 5 s after load.

### Memory — idle trim now reclaims entry and index slack (pending live benchmark)

- The maintenance step queued a fresh sample of keys and only then read the queue depth to decide how much slack justifies a compaction, so every pass saw a backlog and applied the strict backlog thresholds (40% entry slack, 25% index slack). Stores of plain strings never reached them: 1M counters sat at 26% entry slack and 11% index slack forever. The depth is now read before sampling, and the idle thresholds are 12.5% entry slack and 6.25% index slack.
- Local runs through the server (1M keys, bytes per key once the trim has run, about 5 s after the load): counter 67.7 -> 59.1 (Redis in the lab: 54.1), uuid 93.3 -> 84.6, random 149.6 -> 140.8, text 149.4 -> 140.9. Hash, list and set profiles are unchanged.

### Memory — maintenance runs every 2 s for a minute after writes (pending live benchmark)

- The optimizer's maintenance step (idle trim of free blocks, index slack and indexed-container headroom) ran on a fixed 10 s tick and then needed 2 s of write quiet, so memory left behind by a write burst was reclaimed 10 s or more after the burst. The tick is now 2 s while foreground writes happened in the last 60 s and stays 10 s on an idle store.
- hash-large (1000 fields of 64 B, 1M items) goes from 114.1 to 65.9 bytes/item after the trim, which now lands about 5 s after the load instead of about 10 s. The lab's convergence check waits for 12 s of flat memory, and the trim had not fired yet in that window on the lab machine, so it reported the pre-trim 114.3 as settled.

### Memory — hashes stay packed up to 128 fields (pending live benchmark)

- Hashes promote to the indexed layout at 128 fields instead of 32 (and 128 instead of 16 for single-field HSET). Batched HSET to one key already rewrites the packed form once per batch, and the packed/shaped form has no slot table or payload reserve. 100-field hash of 64-byte values (1M items through the server): 117.3 -> 76.0 bytes/item right after load; settled stays 66.4 (Redis in the lab: 82.5 after load).
- Local runs: HGET throughput about 560k -> 650k ops/s, HSET load about 575k -> 510k ops/s. The same threshold change for lists was tried and dropped: it halved load throughput for a small memory gain.
- Tests that expected 100-field hashes to be indexed now use 200 fields; a new model-checked test crosses the threshold with single and batched writes, overwrites and deletes.

### Memory — fixed-width sets stay in the front-coded layout up to 128 members (pending live benchmark)

- Sets whose members all have the same length (IDs, UUIDs, fixed-width tokens) were promoted to the hash-table layout at 32 members. That layout spends a 4-byte slot table sized at 2.5 slots per member plus a 25% payload reserve, about 18 bytes per member on top of the member itself. They now stay in the existing front-coded layout (shared prefixes stored once, no table) until 128 members, the same size where Redis switches away from its compact encoding. Mixed-width sets still promote at 32, because their packed form is decoded member by member on every write.
- Local run through the server, 1M x 24-byte members, 100 per set: footprint right after load 58.6 -> 26.9 bytes/member, settled 49.7 -> 22.1 (Redis 7.0 lab figure: 31.3). Members in that benchmark share prefixes; unrelated random members of the same width compress less but still avoid the table and reserve.
- Lookup no longer copies or allocates: it walks the front-coded members and skips any whose shared-prefix length shows they sort below the target. Re-adding an existing member returns before any rewrite. Microbench at 100 members: SISMEMBER 174 -> 333 ns (about 5% fewer ops/s through the server with pipelining), SADD of an existing member 311 -> 347 ns, SMEMBERS 20.7 -> 5.4 us, SRANDMEMBER 19.9 -> 6.4 us; the load phase got faster (672k -> 723k SADD/s).

### Memory — 16-bit list offsets and allocator-aligned large blocks (pending live benchmark)

- Indexed lists with a payload under 64 KiB now store 16-bit element offsets instead of 32-bit ones (two bytes per element saved); larger lists keep 32-bit offsets, and growth and trimming convert between the two. A 100-element list of 64-byte values trims to 6,715 bytes.
- Blocks above half a segment own a Go allocation, and Go rounds every request up to its own size class (or 8 KiB pages above 32 KiB). The arena used 12.5% geometric classes for them, so a 7,168-byte block cost 8,192 bytes of heap while being accounted as 7,168. Dedicated blocks now use the allocator's classes, so the accounting equals the real cost and a value that just fits a class uses it (the 100-element list above lands in the 6,784-byte class instead of 8,192).
- Local runs through the server (1M items, process RSS after settling): list-medium 76.8 -> 68.9 bytes/item (Redis 7.0 on the same machine: 73.6), RSS 109.5 -> 99.3 MB; list-large 76.9 -> 73.9, RSS 102.8 -> 96.5 MB; hash-large 68.6 -> 65.9, RSS 97.1 -> 89.3 MB; hash-medium 68.4 -> 66.4; set-large 43.2 -> 42.5. Right-after-load figures are unchanged for lists, slightly better for list-large (88.5 -> 85.3), and worse for hash-medium (111.7 -> 117.2) because values grown through the coarse 4.8-8 KiB classes park larger dead blocks until idle compaction.

### Memory — index tables grow by a quarter instead of doubling (pending live benchmark)

- Shard index tables doubled whenever they passed 80% full, so a shard that stopped growing just after a doubling held nearly twice the slots it needed until idle compaction ran (1M counters over 256 shards: 33.6 bytes/key of index against 20.1 once tight). From 256 slots up they now grow to fit a quarter more keys than they hold, which keeps the table within about 25-55% slack.
- Local run, 1M counter keys through the server: footprint right after load 78.8 -> 67.6 bytes/key (Redis 7.0 on the same machine: 88.4); settled memory is unchanged (about 59 bytes/key). Load throughput and p99 were within run-to-run noise (about 660-700k ops/s, p99 47-51 us). Filling one 3,906-key shard takes about 1.2 ms against 0.5 ms, since the table is rebuilt more often.

### Memory — no optimizer sidecar for freshly inserted keys (pending live benchmark)

- The batched fresh-SET path gave every new optimizable value a 24-byte activity sidecar plus an 8-byte slot, although a new key has no write history and nothing reads the sidecar until the optimizer rewrites it. Fresh inserts now start without one; a successful rewrite allocates it when it is needed (JSON-shape candidates), and overwrites still get one, so write-heavy detection is unchanged.
- Fresh RAW values without a sidecar also take the zero-copy direct GET path instead of the activity-tracking one.
- Local run, 1M session-json keys through the server: footprint right after load 525 -> 501 bytes/key (Redis: 502); settled memory is unchanged (about 248 bytes/key).

### Memory — 16-byte arena classes between 385 and 512 bytes (pending live benchmark)

- Arena blocks in the 385-512 byte range were rounded up to 64-byte steps, so a session-json record (384-byte value + key + header, 407 bytes) used a 448-byte block. They now use 16-byte steps (416 bytes there); the established 448 and 512 classes keep their buckets and the new intermediate sizes use appended buckets, so no existing class numbering shifts.
- Engine probe of the session-json load shape (200,000 keys): footprint right after load drops from about 509 to about 464 bytes/key; settled memory is unchanged (about 257 bytes/key).

### Memory — reclaim dead arena blocks left by growing values (pending live benchmark)

- Idle compaction now also runs when the arena holds at least 4 MiB and 10% dead blocks (it previously needed 25%). Lists and other values grown by repeated pushes leave freed blocks in shared segments that no later allocation matches; the lab's list-large run sat at 13% dead, under the old trigger, and never reclaimed it.
- The 4 MiB floor stays above the partially filled tail segment each shard keeps, so a compacted store does not ask for another pass. A new test builds 1,000-item lists, checks one compaction is requested and none after it.
- Engine probe, 200,000 items in 1,000-item lists: 110.4 -> 77.1 bytes/item after the pass (lab list-large before: 88.4).

### Performance — in-place LPOP/RPOP and range-limited LRANGE on indexed lists (pending live benchmark)

- LPOP/RPOP on an indexed list now edit the list in place (offset-table shift for the left end, tail reclaim for the right end) instead of decoding every element and re-encoding a packed copy. A 10,000-element queue pop+push drops from about 3.6 ms to about 1 µs in the engine benchmark.
- Pops no longer demote a large list to the packed form, so LINDEX stays O(1) afterwards. Lists fall back to the packed form below 16 elements.
- LRANGE on an indexed list reads only the requested records. LRANGE 0 9 on 10,000 elements drops from about 820 µs to about 1 µs.
- Regrow and compaction trim drop dead records left at the front of the payload by left pops.

### Memory — smaller indexed sorted sets (pending live benchmark)

- Compaction now trims idle indexed sorted sets (payload headroom and dead records), as it already does for lists.
- Indexed zset records store integer scores as 1-4 byte varints instead of a fixed 8 bytes; non-integral scores keep the IEEE-754 form.
- Indexed zsets whose payload capacity fits in 16 bits use 2-byte slot offsets (header version 6) instead of 4 bytes.
- Plain ZADD on small packed sorted sets reuses pooled decode/encode buffers: 1,641 ns and 3 allocations per op drop to about 1,230 ns and none in the engine benchmark (10 members).
- A new member that sorts after every existing member of a small packed sorted set is appended in place (one scan, no decode, no re-sort): 10-member ZADD drops to about 730 ns in the engine benchmark, from 1,640 ns.
- ZSCORE on small packed sorted sets scans without allocating (10-member lookup: about 704 ns and 28 B/op down to about 530 ns and 0 allocations in the engine benchmark).
- Growing an indexed sorted set (and trimming it when idle) rebuilds straight from the live records: no decode, map or sort, and no per-record allocation. In a profile of the 100-member lab workload the old rebuild path was about 17% of server CPU.
- Per-command client bookkeeping no longer allocates when a connection repeats the same command (it was about 14M allocations in one lab run).
- Pipelined SET batches encode and classify values before taking shard locks. A 256-command batch holds every shard it touches (often most of the 256), so that work used to lengthen other connections' waits; in a profile of the counter load it was 83% of all mutex delay. Engine benchmark with 8 concurrent batch writers: batch p50 about 1.4 ms down to about 0.3-0.5 ms, throughput about 25-30% higher.
- Physical formats are internal; persistence still uses the logical form.

### Memory — trim idle list headroom during compaction (pending live benchmark)

- `Compact` now rewrites idle indexed lists (32+ elements) to exact size, dropping append headroom (payload slack and the power-of-two offset table). Engine-level settled size for 64 B values: 100 items/list 83.1 -> 76.9 B/item, 1000 items/list 86.7 -> 77.1 B/item. A later RPUSH regrows the list on demand.

### Performance — RPUSH on indexed lists (pending live benchmark)

- Growing an indexed list (32+ elements) now copies the stored bytes instead of decoding and re-encoding every element; 1000-element pushes about 2x faster with zero allocations. Evidence and remaining gaps: `docs/LIST-THROUGHPUT-AUDIT.md`.

### Implemented — SLOWLOG and persistence controls (pending review)

- Added audited SLOWLOG operations, client identity capture, SAVE/BGSAVE/BGREWRITEAOF controls, one-off AOF export, background status/error reporting, and queued save/rewrite handoffs.
- Prevent active-AOF writes from being lost between export and replacement by holding the durability lock; writes wait during rewrite.
- Full local race/vet/RESP-fuzz gates passed according to the operator. Live reference: Redis 8.10.2; six-field SLOWLOG targets Redis 8.2. Limits and evidence: `docs/SLOWLOG-PERSISTENCE-AUDIT.md`.

### Verified — graceful-restart Redis PSYNC continuation

- SnugKV now persists upstream replid/offset/stream-mode state across graceful restart when AOF or snapshot durability is configured. The shutdown path first checkpoints the exact replica dataset, then atomically stores the PSYNC continuation tuple. Live Redis validation resumed with partial synchronization from the next-byte offset and continued replicating new writes after restart. Crash-resume continuity remains a documented future hardening item. Audit: `docs/REPLICATION-RESTART-PSYNC-AUDIT.md`.


### Verified — Redis hash template RDB compatibility

- Added Redis 8.10 hash-template interoperability for RDB types 29–32 and opcode 242, including live diskless full-sync validation for template-listpack and template-array reference formats and live HIMPORT propagation via zero-checksum RESTORE payloads. SnugKV materializes the decoded result as a native HASH. Audit: `docs/REPLICATION-HASH-TEMPLATE-AUDIT.md`.


### Verified — Redis HFE LISTPACK_EX RDB compatibility

- Added Redis RDB HFE LISTPACK_EX decoding for types 23/25. Live Redis 8.10.2 validation confirmed a real `listpackex` hash full-syncs into SnugKV with exact absolute field-expiration timestamps and persistent fields preserved. Also hardened Redis replication RESP parsing against fragmented CRLF terminators using `io.ReadFull`, fixing a live 14-byte PING reconnect loop.


### Verified — Redis hash field expiration

- Added Redis-compatible hash-field expiration commands with NX/XX/GT/LT semantics, Redis RDB HFE hashtable metadata decoding (types 22/24), and live Redis 8.10.2 validation of exact absolute field expirations. Also fixed Redis partial-resync reconnect handling so `+CONTINUE` preserves Redis RESP stream mode. Audit: `docs/HASH-FIELD-EXPIRATION-AUDIT.md`.


### Verified — broader Redis RDB encoding compatibility

- Redis full-sync import now supports additional plain and legacy encodings: LIST, SET, HASH, textual-score ZSET, ZSET_2, HASH_ZIPMAP, LIST_ZIPLIST, ZSET_ZIPLIST, HASH_ZIPLIST, and legacy LIST_QUICKLIST. Live Redis 8.10.2 validation confirmed plain HASH/SET/ZSET_2, integer strings, LZF, and TTL restoration. Audit: `docs/REPLICATION-RDB-ENCODINGS-AUDIT.md`.


### Verified — TLS replication

- Redis primary -> SnugKV replica TLS is now supported with mandatory CA verification, hostname/SNI verification, TLS 1.2 minimum, optional client certificates for mTLS, and unchanged plain TCP behavior when disabled. Live native Redis validation covered TLS + password auth, mTLS, live propagation, and READONLY semantics. Audit: `docs/REPLICATION-TLS-AUDIT.md`.


### Verified — authenticated Redis replication

- Redis 8.2 primary authentication is now supported and live-validated for both password-only `AUTH <password>` and ACL `AUTH <username> <password>` upstream handshakes. SnugKV exposes Redis-compatible `masterauth` / `masteruser` settings via config, environment, and CLI. Audit: `docs/REPLICATION-AUTH-AUDIT.md`.


### Verified — Redis diskless EOF full sync

- Redis 8.2 diskless replication using `$EOF:<40-byte marker>` is now supported and live-validated. SnugKV advertises EOF capability, imports the framed RDB without consuming the following command stream, preserves TTLs, continues live replication, and remains READONLY as a replica. Audit: `docs/REPLICATION-DISKLESS-EOF-AUDIT.md`.


### Verified — Redis RDB full-sync interoperability

- Redis 8.2.9 primary -> SnugKV replica full sync now imports real length-prefixed Redis RDB snapshots for the audited core encodings, preserves absolute TTLs, continues with the live Redis command stream, and retains replica READONLY behavior. Audit: `docs/REPLICATION-RDB-FULLSYNC-AUDIT.md`.

- Added Redis-compatible CLIENT-side caching/tracking for the audited single-node surface: `CLIENT TRACKING`, `CLIENT CACHING`, `CLIENT GETREDIR`, BCAST/PREFIX, OPTIN/OPTOUT, NOLOOP, REDIRECT, RESP3 invalidation pushes, and broken-redirect notification semantics.

All notable changes to SnugKV will be documented in this file.

## [Unreleased]

### Memory — idle maintenance collapses HOT hashes (pending live benchmark)

- Hashes above 256 fields become HOT (mutable sidecar, ~131 B/field in the engine probe) and only returned to the compact form (~69 B/field) when a compaction pass ran. HOT-hash memory is neither arena nor index slack, so no maintenance trigger ever fired for it and hash-large stayed at ~114 B/field indefinitely. Maintenance now compacts once the store has been write-idle past the freeze window and at least 1 MiB of HOT sidecar exists.

### Memory — large freed arena blocks are returned to the heap (pending live benchmark)

- A value that outgrows its block (every RPUSH/APPEND-style rewrite) used to leave the old block on a per-size free list. A growing list passes through every size class once, so each shard kept one idle block per class that no other value could reuse. Freed blocks that own a whole segment (above 4 KiB) are now released to the Go heap and the segment slot is reused. Right after loading 1000-item lists this cuts reserved arena from ~174 to ~110 B/item in the engine probe.

### Performance — optimizer yields to foreground writes (pending live benchmark)

- Optimizer workers now stay idle for the whole foreground write burst (bounded at 2 s, or earlier when the queue is more than half full) instead of doing one key every 50 ms per worker, and the quiet poll reuses one timer instead of allocating one per poll. Shard write locks and CPU taken by `Rewrite`/`MarkOptimizationAttempt` during load were showing up as write p99. Catch-up still runs at full speed as soon as writes stop.

### Memory — tight shard index after compaction (pending live benchmark)

- Shard indexes no longer have to be a power-of-two size. Lookups map the hash onto any capacity with a multiply and shift, and compaction rebuilds each index at the smallest capacity that keeps the existing 80% occupancy ceiling. A counter-style workload (1M keys, about 3,900 per shard) was holding 8,192 slots per shard (33.6 B/key); it now needs about 4,900 (20 B/key). Growth after a rebuild still doubles.
- Idle maintenance now also triggers compaction when the index reservation is well above what the live keys need, not only when the arena or entry array is loose. Compaction still only runs while writes are quiet.
- `rediswirebench` convergence no longer returns immediately for workloads that queue no optimizer work (counter, uuid, ints); it waits for memory to stay stable so idle compaction is measured. First live run on this branch: counter settles at 59.1 B/key (index 20.1, was 33.6) against 78.8 before.

### Added

- Post-merge project-wide validation for the distributed hardening milestone passed
  on `main`: `go test ./... -count=1`, `go test -race ./... -count=1`, and
  `go vet ./...`. The audited distributed scope is now documented as
  production-candidate.
- Completed the audited distributed production-hardening matrix: deterministic
  MIGRATE sliding-timeout/partial-durability regressions, 5/5 repeated-recovery
  stress runs, 10/10 failover-restart stress runs, and a retained 6-cycle / 42-case distributed soak with zero failures/timeouts. An earlier extended soak
  completed 70 consecutive cases before exposing the migration timeout defect
  fixed by this branch.
- Distributed hardening now includes a dedicated internal cluster-control credential (`cluster_control_auth`), connection-scoped `SNUG.INTERNAL AUTH`, peer-only RPC gating, and revocation on AUTH/HELLO/RESET.
- Cluster/sharding core includes Redis-compatible slot routing, MOVED/ASK/ASKING, guarded reshard/recovery, membership, replica-aware shard topology, failover ownership convergence, health/consistency views, and stale-coordinator/write-fencing protections.
- Added `docs/PROJECT-STATE.md` as the canonical maintainer/agent handoff for current implementation status and remaining hardening work.
- Redis 8.10-audited JSONPath support across member/index selectors, wildcards, recursive descent, slices/unions, scalar/logical/regex filters, membership/set operators, size/empty predicates, arithmetic and function expressions, multi-match updates/deletes, and AOF restart recovery. Object insertion order and exact error wording remain documented compatibility boundaries.
- Redis-compatible legacy `GEORADIUS` and `GEORADIUSBYMEMBER` aliases, including COUNT/ANY, WITHDIST/WITHHASH/WITHCOORD, STORE/STOREDIST, Redis 8.2 error compatibility, and dynamic source/destination key discovery.
- Redis-compatible `SCRIPT DEBUG YES|SYNC|NO` connection state with LDB continue/end-session wire framing, async debug execution on a disposable logical store clone, and SYNC persistence on the real dataset. Full line stepping/breakpoints remain explicitly deferred because GopherLua lacks debug hooks.
- Redis 8.2 Lua eviction-policy parity for `allkeys-lru` and `volatile-lru`, including policy-preserving nested eviction/retry, persistent-key protection for volatile eviction, and `allow-oom` fallback only after eligible victims are exhausted.
- Redis 8.2 Lua Eval shebang/OOM flag parity for the audited noeviction surface, including legacy first-write admission, `#!lua` metadata parsing, `allow-oom`, `no-writes`, `_RO` enforcement, SCRIPT LOAD/EVALSHA propagation, and live differential coverage.
- Redis 8.2 command-level OOM admission parity for the audited noeviction surface, including exact `denyoom`/`fast` COMMAND flags, pre-execution rejection for denyoom commands, execution of non-denyoom shrinking/mutating commands while already over maxmemory, exact OOM error text, and a live differential harness.
- Redis 8.2 Function `allow-oom` flag with OOM-entry gating, scoped nested-command
  memory-admission bypass, `no-writes` interaction, FUNCTION LIST exposure, and
  live Redis differential coverage.
- Core RESP3 support with per-connection `HELLO 3` negotiation, `HELLO 2` switching,
  protocol-aware null/map/set/double/verbatim reply shapes, nested COMMAND/ACL
  RESP3 structures, and classic/pattern/sharded Pub/Sub push frames while
  preserving RESP2 behavior.
- Redis 8.2-shaped COMMAND tooling for the implemented surface, including ten-field
  `COMMAND INFO`, `COMMAND DOCS`, `GETKEYS`, `GETKEYSANDFLAGS`, parent/
  subcommand metadata, and dynamic key extraction for variable-key commands.
- Common CONFIG tooling compatibility: `CONFIG GET`, `SET`, `RESETSTAT`,
  `REWRITE`, and `HELP`, with live maxmemory/policy/maxclients/appendfsync
  mutation and atomic strict-JSON rewrite/restart persistence.
- Redis-style authentication and ACL management: `AUTH`, `ACL WHOAMI`, `USERS`,
  `GETUSER`, `LIST`, `SETUSER`, `DELUSER`, `CAT`, `DRYRUN`, `GENPASS`,
  `LOG`, `SAVE`, `LOAD`, and `HELP`.
- ACL command/category/key/channel-pattern enforcement, root-or-selector rule-set
  evaluation, MULTI queue-time ACL dirtying, EXEC-time re-authorization,
  aggregated ACL LOG entries, and optional ACL-file persistence/startup restore
  with fail-closed malformed-file handling.
- Redis-compatible ACL SETUSER hardening for reset/resetpass/nopass, password and
  hash removal, hash validation, command aliases, sanitize-payload flags, channel
  modifiers, and selector parsing/serialization.
- Redis Functions core with `FUNCTION LOAD/LIST/DELETE/FLUSH`, `FCALL`,
  `FCALL_RO`, read-only enforcement, function-local Lua state, and `no-writes`.
- `FUNCTION DUMP` / `FUNCTION RESTORE` with checksum-protected versioned payloads,
  default `APPEND`, plus `FLUSH` and `REPLACE` restore policies.
- `FUNCTION STATS` with live running-function metadata and Lua engine
  library/function counts, plus Redis-style `FUNCTION HELP` output.
- Durable Redis Function library restoration across restart when AOF or snapshot
  persistence is configured. SnugKV stores the current function registry in an
  atomic sidecar next to the configured persistence file.
- Native HASH datatype with packed SH1 storage, adaptive shared field-shape storage,
  numeric operations, scan, random-field support, TTL/rename integration, and
  logical persistence.
- Native SET datatype with canonical packed storage, adaptive singleton/prefix
  physical forms, membership, scan, algebra/store, move, pop, and random-member
  commands.
- Native LIST datatype with packed ordered storage, compatibility mutations,
  atomic `LMOVE`/`RPOPLPUSH`, and blocking `BLPOP`, `BRPOP`, `BLMOVE`, and
  `BRPOPLPUSH` waiter/wakeup support.
- Native ZSET datatype with adaptive integer score delta encoding, member front
  coding, rank/score/lex ranges, algebra/store commands, pop/random/scan commands,
  `ZRANGESTORE`, and blocking pop operations.
- Mixed SET/ZSET `ZUNION`/`ZINTER` inputs with `WEIGHTS` and
  `AGGREGATE SUM|MIN|MAX|COUNT`.
- `ZPOPMIN`, `ZPOPMAX`, `ZMPOP`, `ZMSCORE`, `ZRANDMEMBER`, `ZSCAN`, and
  `ZRANGESTORE`, including dynamic durability-key handling for multi-key pops.
- `BZPOPMIN`, `BZPOPMAX`, and `BZMPOP` with fractional timeouts, per-key
  waiter/wakeup signaling, Redis-compatible RESP2 reply shapes, and shutdown
  cancellation for infinite waits.
- Per-connection cancellation plumbing for blocking LIST/ZSET commands and Linux
  TCP peer-disconnect detection that does not consume queued RESP bytes.
- Dedicated HASH, SET, LIST, and ZSET benchmark harnesses and documented 100k-key
  memory comparison matrices.
- RESP2 TCP server with bounded protocol parsing.
- Sharded in-memory storage engine.
- Expiration and TTL operations.
- Memory limits and inspection commands.
- Adaptive canonical encodings for selected scalar value types.
- Optional JSON-shape encoding and compression candidates.
- Logical persistence with checksum-protected frames.
- Separate admin listener for `SNUG.*` diagnostics and controls.
- Compatibility smoke tests for ioredis, node-redis, redis-py, and go-redis.
- TCP client soak harness and engine soak workload.
- AGPL-3.0 licensing with separate commercial-license terms available.

### Changed

- Reworked scalar memory layout for tiny values: encoded integer/unsigned/float/
  timestamp payloads up to 8 bytes can live inline in the existing arena reference,
  avoiding arena allocation while preserving generation/version checks.
- Reduced stored entry records from 32 bytes to 24 bytes by moving optional
  activity/schema metadata to a lazy per-shard sidecar; metadata-free workloads
  allocate no metadata slot array.
- Extended compaction to reclaim dense entry-array over-capacity. On the recorded
  1M-key 10-byte counter development dataset, explicit compaction reduced
  engine-accounted memory from 77.13 B/key to 72.61 B/key, near the 72.39 B/key
  Redis reference for that exact run.
- Removed foreground JSON-shape parsing/encoding from SET; shape learning and
  representation rewrites stay in the background optimizer so SET latency does
  not scale with JSON representation complexity.

- Optimized the Redis-wire plain SET/load path: concurrent benchmark load workers,
  reusable buffered SET parsing, transient raw-clone removal, reusable optimizer
  candidate scratch, borrowed optimizer raw fallbacks, known-hash indexed
  publication, and backlog-aware optimizer CPU yielding. On the recorded
  1M-key/256-byte-random/8-worker development comparison, SnugKV reached a
  five-run median of ~315k SET/s versus ~318k/s for the Redis 8.2 reference while
  using ~7.7% fewer engine-accounted bytes per key.

- Native container formats are now excluded from the generic scalar optimizer.
- HASH, SET, LIST, and ZSET storage designs are frozen for v1; further memory work
  is directed toward shared index/entry/shard/arena overhead.
- Documentation now reflects the implemented native datatype command surface and
  current compatibility boundaries.

### Hardened

- JSON ACL compatibility now matches Redis 8.10 for `@json`/read/write category enforcement, JSON key-pattern checks, and Redis's first-key-only `JSON.MGET` ACL visibility; `JSON.MSET` continues to authorize every referenced key.
- Dynamic ACL enforcement for nested Lua/Function command execution, including
  caller ACL propagation through EVAL/EVALSHA, EVAL_RO/EVALSHA_RO, FCALL, and
  FCALL_RO. Wildcard external SORT BY/GET now matches Redis 8.2 by requiring
  full key scope in one complete root rule set or selector.
- `FUNCTION STATS` bypasses the normal durability mutex so it remains observable
  from another client while an FCALL is running.
- Function dump payload checksum validation and atomic restore-policy validation;
  corrupt payloads do not modify the current function registry.
- Function persistence uses atomic temp-file replacement plus file/directory fsync,
  and startup rejects corrupt durable function state.
- Atomic max-memory rollback for native container mutations and multi-key stores.
- AOF restart coverage for HASH, SET, LIST, and ZSET mutations.
- Blocking LIST and ZSET commands wait outside the durability mutex.
- Blocking ZSET waits register before readiness checks to avoid lost wakeups.
- Linux blocked clients are removed when the TCP peer half-closes/hangs up, even
  when pipelined bytes are already queued behind the blocking command.
- Dynamic durability snapshots for `ZMPOP` candidate keys.
- Legacy string/numeric/bitmap commands now reject native HASH/SET/LIST/ZSET keys
  with Redis-style WRONGTYPE instead of decoding packed container bytes.
- Redis-specific scalar exceptions are preserved for `MGET`, `GETDEL`, plain
  `SET`, and `BITOP` destination overwrite behavior.
- TCP error framing preserves the `-WRONGTYPE` RESP error prefix.
- Large arena allocations up to the RESP bulk-size boundary.
- Connection-level panic recovery.
- Exact RESP maximum-bulk boundary behavior.
- Lazy JSON-shape store allocation.
- Persistence restart and corruption recovery tests.
- Redis 8.2 Streams edge-case parity fixes for XACKDEL dangling-reference status,
  trim lifetime metadata, XRANGE COUNT 0, XTRIM LIMIT/negative-MAXLEN errors,
  group ENTRIESREAD/lag handling, DELCONSUMER PEL cleanup, inactive consumer
  metadata, empty XPENDING replies, and BUSYGROUP/NOGROUP error classes.

### Verified

- Replication Phase 2 ACK/observability adds `REPLCONF ACK` handling, monotonic per-replica acknowledged offsets, lag reporting in `INFO replication`, periodic replica ACK emission, and exact Redis 8.2 live differential parity for the audited slice.

- Replication Phase 2 core adds a bounded backlog, Redis next-byte PSYNC offset semantics, reconnect continuation via `+CONTINUE`, full-resync fallback for unserviceable offsets, replica-side upstream replid/offset reuse, and exact Redis 8.2 live differential parity for the audited slice.

- Replication Phase 2 core adds a bounded backlog, Redis next-byte PSYNC offset semantics, reconnect continuation via `+CONTINUE`, full-resync fallback for unserviceable offsets, replica-side upstream replid/offset reuse, and exact Redis 8.2 live differential parity for the audited slice.

- Replication Phase 1 adds SnugKV primary/replica support with Redis-shaped REPLICAOF/ROLE/INFO replication/PSYNC control flow, full logical snapshot sync, live logical-frame propagation, TTL preservation, replica read-only enforcement, disconnect cleanup, promotion, transaction replication, and exact live oracle parity for the audited surface.


- TimeSeries Phase 1 adds native persistent RedisTimeSeries-compatible series with CREATE/ADD/GET/RANGE/REVRANGE/INCRBY/DECRBY/DEL/INFO, retention, duplicate policies, labels, NaN/out-of-order semantics, TTL/persistence, ACL/OOM metadata, and exact live differential parity for the audited slice.


- t-digest Phase 1 adds native persistent RedisBloom-compatible sketches with CREATE/ADD/MERGE/RESET/MIN/MAX/QUANTILE/CDF/RANK/REVRANK/BYRANK/BYREVRANK/TRIMMED_MEAN/INFO, centroid buffering/compression state, TTL/persistence, ACL/OOM metadata, and exact live differential parity for the audited slice.


- TopK Phase 1 adds native persistent RedisBloom-compatible heavy-hitter tracking with RESERVE/ADD/INCRBY/QUERY/COUNT/LIST/INFO, ejection semantics, TTL/persistence, ACL/OOM metadata, and exact live differential parity for the audited slice.


- Count-Min Sketch Phase 1 adds native persistent CMS values with audited INITBYDIM/INITBYPROB/INCRBY/QUERY/MERGE/INFO behavior, weighted merges, packed 32-bit counters, TTL/persistence, ACL/OOM metadata, and exact live RedisBloom differential parity for the audited slice.


- Cuckoo filter Phase 1 adds a native persistent RedisBloom-compatible fingerprint filter with reserve/add/addnx/insert/insertnx/exists/mexists/count/delete/info, TTL preservation, ACL/OOM metadata, and exact live differential parity for the audited slice.


- Bloom filter expansion adds RedisBloom-compatible scalable generations, `EXPANSION`, `NONSCALING`, inline per-item overflow errors, and exact audited `BF.INFO` capacity/size/filter metadata. The live expansion oracle matched RedisBloom line-for-line except for the target/port label.


- Bloom filter Phase 1 adds native persistent Bloom values with `BF.RESERVE`, `BF.ADD`, `BF.EXISTS`, `BF.MADD`, `BF.MEXISTS`, `BF.CARD`, `BF.INFO`, and audited `BF.INSERT ... CAPACITY ... ERROR ... ITEMS` support. A live differential against RedisBloom on port 6392 matched line-for-line for the audited surface, including error and wrong-type behavior.


- Optimizer convergence now periodically resamples missed candidates and automatically compacts dense entry storage when slot slack is material. `Compact()` rebuilds live entries contiguously instead of retaining deleted entry holes. A 200,000-key / 50,000-live-key delete-heavy probe reduced entry capacity from 225,091 to 50,000 and entry storage from 5,402,184 bytes to 1,200,000 bytes (~77.8% reclaimed).


- Search index creation now builds an online generation: `FT.CREATE` snapshots JSON one shard at a time, journals concurrent matching mutations/deletes, replays them into the pending generation, and atomically publishes the finished index without holding all primary shards locked for the full backfill.


- Redis Search vector Phase 1 for JSON indexes: `VECTOR FLAT` schema fields with `FLOAT32`, fixed `DIM`, `COSINE`, binary `PARAMS`, KNN, `VECTOR_RANGE`, score aliases, vector-aware `RETURN`, explicit `SORTBY score`, and audited error handling. Unsorted/tied vector result order and Redis-only `FT.INFO` implementation statistics are not treated as compatibility requirements.


- Redis Search `FT.AGGREGATE` core pipeline over JSON indexes, including base queries, `LOAD`, `FILTER`, `GROUPBY`, `REDUCE COUNT|SUM|MIN|MAX|AVG`, aggregate `SORTBY`, `LIMIT`, DIALECT 1/2, and audited parser/error behavior. Remaining live-diff noise is limited to Redis's incidental unsorted row/group ordering and empty-row formatting.


- Redis Search GEO fields on JSON, including `"lon,lat"` string indexing, `m`/`km`/`mi`/`ft` radius filters, boolean composition, mutation visibility, `SORTBY`, `LIMIT`, `SORTABLE`, `NOINDEX`, coordinate validation, parser errors, and DIALECT 1/2 behavior. Remaining live-diff noise is the known narrow `FT.INFO` surface plus deterministic unsorted ordering/`LIMIT` selection.


- Redis Search BM25STD-style relevance scoring and `WITHSCORES`, including field weights, stemming/fuzzy/phonetic expansion scoring, phrase/proximity scoring, wildcard `*` normalization, `SORTBY` precedence, `LIMIT`, `RETURN`, duplicate option handling, and shared-posting field-mask semantics. Live Redis differential differences are limited to tiny floating-point rounding, JSON object key order, and equal-score tie ordering.


- Redis Search PHONETIC `dm:en` support for TEXT fields, including unqualified lookup, NOSTEM interaction, schema ordering, duplicate handling, and audited parser errors.

- Redis Search broader TEXT wildcard compatibility: suffix (`*ory`), contains (`*mor*`), grouped forms, escaping, parser boundaries, and measured fuzzy/wildcard syntax errors.

- Redis Search unqualified TEXT queries across all indexed TEXT fields, including multi-term AND, phrases, prefix/fuzzy queries, stopwords, stemming/NOSTEM, and mixed fielded clauses.

- Redis Search schema modifiers: `WEIGHT`, `SORTABLE`, and `NOINDEX`, including audited ordering/duplicate behavior and TEXT sorting.

- Redis Search fuzzy TEXT compatibility for `%term%`, `%%term%%`, and `%%%term%%%`, including grouped fuzzy terms, stem interaction, and parser error classes.

- Redis Search differential coverage for grouped TEXT `SLOP`/`INORDER` semantics, quoted-phrase exactness under those options, and compatible parse/argument errors.

- Redis Search language differential coverage for `LANGUAGE`, query `LANGUAGE`, and `LANGUAGE_FIELD` with English/German stemming and Redis-compatible invalid-language error classes.

- Redis 8.2 Search differential coverage for JSON TEXT indexing, multi-term groups, prefixes, exact phrases, English stemming/NOSTEM, and default/disabled/custom stopword modes; remaining diffs are unsorted result ordering and JSON object field order.

- Redis 8.2 Function allow-oom differential audit covering plain FCALL rejection
  while already OOM, allow-oom reads/writes, no-writes entry behavior,
  FCALL_RO behavior, deletion while OOM, and post-invocation bypass restoration.
- Dynamic ACL differential audit against Redis 8.2 covering nested command/key
  denial in Lua and Functions, selector atomicity, wildcard external SORT BY/GET
  denial, all-key selector success, and nested SORT inside Lua. Remaining
  differences are limited to Lua/Function runtime error formatting.
- Real-client RESP3 smoke coverage with ioredis 6, node-redis 6 (including reconnect), redis-py, and go-redis v9. The same harness passes against SnugKV and the Redis 8.2 oracle.
- Redis 8.2 Streams differential audit covering explicit/automatic/partial IDs,
  range bounds, exact/approximate trim grammar, consumer-group creation/SETID/
  ENTRIESREAD/lag, XREADGROUP/XPENDING, XCLAIM/XAUTOCLAIM, XINFO, consumer
  lifecycle, and KEEPREF/DELREF/ACKED reference policies. The only documented
  remaining diffs are implementation-specific approximate-`~` trim granularity
  and Redis-internal radix-tree diagnostic counts.
- Redis 8.2 RESP3 differential audits covering HELLO negotiation/options/errors,
  null-bearing replies, HGETALL maps, SMEMBERS sets, ZSET score doubles/pair
  replies, GEO coordinate doubles, XREAD/XREADGROUP and XINFO maps, FUNCTION STATS
  and CONFIG GET maps, INFO/CLIENT INFO verbatim strings, COMMAND INFO/ACL GETUSER
  nested structures, and RESP3 classic/pattern/sharded Pub/Sub pushes plus ordinary
  commands while subscribed. The final broad structural diff contained only
  expected HELLO module metadata and SCAN dataset/order differences.
- Live Redis 8.2 differential audits for COMMAND metadata/key discovery, CONFIG
  common tooling, and the ACL surface including command/key/category rules,
  channel patterns, SETUSER modifiers, selectors, transaction re-authorization,
  ACL LOG aggregation, ACL SAVE/LOAD persistence, and restart enforcement.
- ACL restart persistence on a live SnugKV process, plus startup rejection for a
  malformed configured ACL file.
- `FUNCTION STATS` tests cover exact idle RESP2 shape, live function
  name/command/duration metadata, engine counts, and non-blocking access while the
  durability mutex is held; `FUNCTION HELP` and arity errors are covered too.
- `FUNCTION DUMP`/`RESTORE` tests cover round trips, APPEND collision rejection,
  REPLACE, FLUSH, checksum corruption, invalid policies, restart restoration, and
  persisted empty registries after `FUNCTION FLUSH`.
- Full race suite, `go vet`, and RESP fuzz are green for the native datatype work.
- Cross-datatype scalar regression tests cover GET/GETSET/GETEX, append/range,
  numeric, bitmap, `SET ... GET`, `MGET`, `GETDEL`, and `BITOP` behavior.
- Blocking ZSET tests cover immediate/wakeup/timeout behavior, key priority,
  `BZMPOP COUNT`, shutdown cancellation, and the AOF durability-lock invariant.
- Blocking disconnect tests verify LIST/ZSET waiter cleanup and real Linux TCP
  connection cleanup, including a pipelined command behind an infinite `BLPOP`.
- Local redis-cli smoke tests validated LIST blocking behavior and ZSET core, range,
  lex, algebra, and store semantics.
- 100k-key native datatype benchmark matrices are recorded in
  `benchmarks/README.md`.
- One-hour engine and TCP/RESP soak runs completed with zero mismatches/client errors
  in the earlier alpha validation cycle.

## [0.1.0-alpha] - 2026-09-14

Initial public alpha release.