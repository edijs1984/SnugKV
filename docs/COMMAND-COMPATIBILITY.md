# COMMAND compatibility

This document records SnugKV's current Redis `COMMAND` introspection surface after the completed Redis 8.2 differential audit.

## Status

The COMMAND compatibility milestone tracked in issue #90 is complete for SnugKV's implemented single-node RESP2 surface.

Implemented introspection commands:

- `COMMAND`
- `COMMAND COUNT`
- `COMMAND INFO [command ...]`
- `COMMAND DOCS [command ...]` for SnugKV's implemented/documented surface
- `COMMAND GETKEYS <command ...>`
- `COMMAND GETKEYSANDFLAGS <command ...>`

The runtime registry currently contains 207 top-level commands after package initialization. Redis 8.2 exposes a larger command count because it implements command families and subcommands that SnugKV intentionally does not expose.

`commandTable` remains the authoritative runtime registry for command validation, arity, fixed key positions, and coarse read/write classification. Introspection metadata builds on that registry and on existing command parsers rather than creating an independent execution registry.

## GETKEYS / GETKEYSANDFLAGS

Fixed-key extraction reuses existing `first` / `last` / `step` metadata. Dynamic extraction is implemented for the audited variable-key families:

- `EVAL`, `EVALSHA`, `EVAL_RO`, `EVALSHA_RO`
- `FCALL`, `FCALL_RO`
- `COPY`
- `BITOP`
- `ZUNION`, `ZINTER`, `ZDIFF`, `ZINTERCARD`
- `ZUNIONSTORE`, `ZINTERSTORE`, `ZDIFFSTORE`
- `ZMPOP`, `BZMPOP`
- `XREAD`, `XREADGROUP`

The final live Redis 8.2 differential matched the sampled key lists and key flags.

Notable compatible classifications include:

- `SET`: `OW update`
- `DEL`: `RM delete`
- `COPY`: source `RO access`, destination `OW update`
- `ZMPOP` / `BZMPOP`: `RW access delete`
- `XREAD`: `RO access`
- `XREADGROUP`: `RO access` key-spec classification, matching Redis even though consumer-group bookkeeping mutates internal group state

## COMMAND INFO

`COMMAND INFO` returns the Redis 7+/8 ten-field shape:

1. command name
2. arity
3. command flags
4. first key
5. last key
6. key step
7. ACL categories
8. tips
9. key specifications
10. subcommands

`COMMAND INFO` with no command names returns all registered top-level SnugKV command metadata.

Direct canonical subcommand lookup is supported using names such as:

- `COMMAND|COUNT`
- `CLIENT|ID`
- `FUNCTION|LOAD`
- `SCRIPT|KILL`

Parent metadata is implemented for `COMMAND`, `CLIENT`, `FUNCTION`, and `SCRIPT`. Parent field 10 advertises only subcommands actually implemented by SnugKV; unsupported Redis-only surfaces such as `CLIENT TRACKING` and deferred `SCRIPT DEBUG` are not fabricated.

The completed INFO differential covers both the original sample and the later broad hardening sample.

Original audited sample:

- `GET`
- `SET`
- `DEL`
- `COPY`
- `EVAL`
- `FCALL`
- `SORT`
- `XADD`
- `PFADD`
- `GEOADD`

Final hardening sample additionally matched Redis 8.2 for:

- `BITOP`
- `EVALSHA`, `EVAL_RO`, `EVALSHA_RO`
- `FCALL_RO`
- `SORT_RO`
- `ZUNION`, `ZINTER`, `ZDIFF`, `ZINTERCARD`
- `ZUNIONSTORE`, `ZINTERSTORE`, `ZDIFFSTORE`
- `ZMPOP`, `BZMPOP`
- `XREAD`, `XREADGROUP`
- implemented `COMMAND|*`, `CLIENT|*`, `FUNCTION|*`, and `SCRIPT|*` child metadata

The audit covered arity, command flags, ACL categories, tips, notes, fixed/dynamic key specs, keyword/range specs, and RESP2 wire types.

Examples of Redis-specific details now represented include:

- `SET` `variable_flags` and optional-GET key-spec note
- EVAL/FCALL worst-case key-usage notes
- read-only EVAL/FCALL `readonly` command flags and RO/ACCESS key specs
- `DEL` multi-shard tips
- `BITOP` destination/source split key specs
- ZSET algebra `keynum` metadata
- ZSET store destination/source split metadata
- `ZMPOP` / `BZMPOP` movable-key and delete semantics
- `XREAD` / `XREADGROUP` incomplete keyword-based STREAMS specs
- `XADD` nondeterministic-output tip and trimming note
- `PFADD` `RW insert`
- `COPY` separate source/destination key specs
- `SORT` / `SORT_RO` fixed source plus unknown pattern/store key specs

## Parent / subcommand metadata

Supported child metadata is stored independently from top-level dispatch while remaining queryable by canonical full names.

Current advertised child surface:

### COMMAND

- `COMMAND|COUNT`
- `COMMAND|INFO`
- `COMMAND|DOCS`
- `COMMAND|GETKEYS`
- `COMMAND|GETKEYSANDFLAGS`

### CLIENT

- `CLIENT|ID`
- `CLIENT|GETNAME`
- `CLIENT|SETNAME`
- `CLIENT|SETINFO`
- `CLIENT|INFO`
- `CLIENT|LIST`
- `CLIENT|KILL`
- `CLIENT|UNBLOCK`
- `CLIENT|HELP`

### FUNCTION

- `FUNCTION|LOAD`
- `FUNCTION|LIST`
- `FUNCTION|DELETE`
- `FUNCTION|FLUSH`
- `FUNCTION|DUMP`
- `FUNCTION|RESTORE`
- `FUNCTION|STATS`
- `FUNCTION|KILL`
- `FUNCTION|HELP`

### SCRIPT

- `SCRIPT|LOAD`
- `SCRIPT|EXISTS`
- `SCRIPT|FLUSH`
- `SCRIPT|KILL`

This intentionally differs from Redis where Redis exposes additional subcommands SnugKV does not implement.

## COMMAND DOCS

`COMMAND DOCS` supports requested command filtering, preserves requested order, omits unknown commands, and returns an empty array when none of the requested commands are known.

The metadata model supports recursive documentary argument trees with:

- `name`
- `type`
- `display_text`
- `token`
- `since`
- `key_spec_index`
- argument flags such as `optional` and `multiple`
- nested `arguments`
- command `history`
- `oneof` and `block` structures

The documented surface was expanded during the final audit to include the previously abbreviated/missing implemented families, including:

- `BITOP`
- EVAL/EVALSHA read/write variants
- FCALL/FCALL_RO
- SORT/SORT_RO
- ZSET algebra/store/pop families
- XREAD/XREADGROUP
- Redis 8.2 XADD `KEEPREF` / `DELREF` / `ACKED` documentary history/options

The earlier rich-documentation parity for `GET`, `SET`, `DEL`, `COPY`, `XADD`, `PFADD`, and `GEOADD` remains.

SnugKV intentionally documents its implemented surface instead of cloning documentation for commands or subcommands it does not support.

## Validation completed

The final COMMAND work passed:

```text
go test -race -count=1 ./internal/server
go test -race -count=1 ./...
go vet ./...
go test ./internal/resp -run '^$' -fuzz FuzzReadCommand -fuzztime=10s
go build ./cmd/snugkv
```

Live Redis 8.2 vs SnugKV differential testing covered:

- fixed and dynamic key extraction
- `GETKEYSANDFLAGS` classifications
- ten-field INFO shape and wire types
- parent/subcommand INFO hierarchy
- direct canonical child INFO lookups
- unsupported-child omission
- command flags, ACL categories, tips, notes and key specs
- DOCS filtering and unknown-command behavior
- recursive argument structures, histories, tokens and key-spec indexes
- the broader INFO hardening sample listed above

## Compatibility boundary

COMMAND introspection is complete for the audited implemented surface, not a claim that SnugKV implements all Redis 8.2 commands.

Important intentional boundaries remain:

- SnugKV exposes 207 top-level runtime commands rather than Redis 8.2's larger registry.
- Unsupported Redis-only CLIENT subcommands are not advertised.
- `SCRIPT DEBUG` remains intentionally deferred until real LDB-style semantics exist.
- Future new commands must add accurate metadata rather than inheriting generic approximations.

Issue #90 is complete. The next client/tooling compatibility target is common `CONFIG` support, followed by ACL/authentication scope.
