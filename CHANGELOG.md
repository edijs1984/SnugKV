# Changelog

All notable changes to SnugKV will be documented in this file.

## [Unreleased]

### Added

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

- Native container formats are now excluded from the generic scalar optimizer.
- HASH, SET, LIST, and ZSET storage designs are frozen for v1; further memory work
  is directed toward shared index/entry/shard/arena overhead.
- Documentation now reflects the implemented native datatype command surface and
  current compatibility boundaries.

### Hardened

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