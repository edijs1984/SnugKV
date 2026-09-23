# Changelog
- Added Redis-compatible CLIENT-side caching/tracking for the audited single-node surface: `CLIENT TRACKING`, `CLIENT CACHING`, `CLIENT GETREDIR`, BCAST/PREFIX, OPTIN/OPTOUT, NOLOOP, REDIRECT, RESP3 invalidation pushes, and broken-redirect notification semantics.

All notable changes to SnugKV will be documented in this file.

## [Unreleased]

### Added

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