# COMMAND compatibility

This document records SnugKV's current Redis `COMMAND` introspection surface and the live differential work completed against Redis on ports 6379/6380.

## Implemented surface

The following introspection commands are implemented:

- `COMMAND`
- `COMMAND COUNT`
- `COMMAND INFO <command ...>`
- `COMMAND DOCS [command ...]` for the implemented/documented subset
- `COMMAND GETKEYS <command ...>`
- `COMMAND GETKEYSANDFLAGS <command ...>`

`commandTable` remains the authoritative runtime registry for command arity, fixed key positions, and coarse read/write classification. New introspection helpers derive Redis wire metadata from that table and from the existing command parsers rather than creating a second execution registry.

## Runtime command inventory

The current runtime registry contains 207 commands after package initialization. The earlier raw `case` grep count is intentionally not treated as a command count because it also captures subcommands and option tokens from nested switches.

## GETKEYS / GETKEYSANDFLAGS

Fixed-key extraction reuses the existing `first` / `last` / `step` metadata. Dynamic extraction is implemented for the currently audited variable-key families, including:

- `EVAL`, `EVALSHA`, `EVAL_RO`, `EVALSHA_RO`
- `FCALL`, `FCALL_RO`
- `COPY`
- `BITOP`
- `ZUNION`, `ZINTER`, `ZDIFF`, `ZINTERCARD`
- `ZUNIONSTORE`, `ZINTERSTORE`, `ZDIFFSTORE`
- `ZMPOP`, `BZMPOP`
- `XREAD`, `XREADGROUP`

The dynamic paths deliberately reuse SnugKV's existing parsers where possible. This keeps key discovery aligned with actual execution grammar rather than duplicating option parsing in the metadata layer.

Live Redis differential testing matched the audited key lists and key flags. Notable Redis-compatible flag behavior includes:

- `SET`: `OW update`
- `DEL`: `RM delete`
- `COPY`: source `RO access`, destination `OW update`
- `ZMPOP` / `BZMPOP`: `RW access delete`
- `XREAD`: `RO access`
- `XREADGROUP`: Redis key-spec metadata is also `RO access`, even though consumer-group bookkeeping can mutate internal stream-group state

## COMMAND INFO

`COMMAND INFO` now returns the Redis 7+/8 ten-field shape:

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

The audited sample has live Redis parity for:

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

The audit covered command flags, ACL categories, tips, key specs, dynamic `keynum` specs, notes, RESP wire types, and Redis simple-string/bulk-string distinctions. Examples of corrected details include:

- `SET` key-spec note and `variable_flags`
- EVAL/FCALL worst-case `RW access update` note
- `DEL` multi-shard tips
- `XADD` `nondeterministic_output` bulk-string tip and trimming note
- `PFADD` key flag `RW insert`
- `COPY` separate source/destination key specs
- `SORT` fixed source plus unknown BY/GET and STORE key specs

Field 10 is currently empty for parent commands. Parent/subcommand metadata for commands such as `CLIENT`, `FUNCTION`, `SCRIPT`, and `COMMAND` is the main remaining `COMMAND INFO` gap.

## COMMAND DOCS

`COMMAND DOCS` supports requested command filtering, preserves requested order, omits unknown commands, and returns an empty array when no requested command is known, matching the audited Redis behavior.

The metadata model supports recursive documentary argument trees, including:

- `name`
- `type`
- `display_text`
- `token`
- `since`
- `key_spec_index`
- Redis-style argument `flags` arrays (`optional`, `multiple`)
- nested `arguments`
- command `history`

The following commands were live-differentially aligned in the audited subset:

- `GET`
- `SET`
- `DEL`
- `COPY`
- `EVAL`
- `FCALL`
- `XADD`
- `PFADD`
- `GEOADD`

This includes the rich nested documentation trees for `SET`, `XADD`, and `GEOADD`, including `oneof` and `block` structures, option tokens, `since` metadata, history arrays, and optional/multiple flags.

`COMMAND DOCS` is intentionally documented only for the current metadata subset rather than fabricating Redis documentation for all 207 registered commands.

## Validation completed

During the implementation slices, focused COMMAND metadata tests were run together with the normal repository gates:

```text
go test -race -count=1 ./internal/server
go test -race -count=1 ./...
go vet ./...
go test ./internal/resp -run '^$' -fuzz FuzzReadCommand -fuzztime=10s
go build ./cmd/snugkv
```

Live Redis 6379 vs SnugKV 6380 differential checks were also performed for:

- fixed and dynamic key extraction
- GETKEYSANDFLAGS key classifications
- ten-field `COMMAND INFO` reply shape and wire types
- selected command flags / ACL categories / tips / key specs
- `COMMAND DOCS` filtering, unknown-command behavior, recursive arguments, histories, and token metadata

## Remaining work before closing issue #90

- audit and implement parent/subcommand metadata for `COMMAND`, `CLIENT`, `FUNCTION`, and `SCRIPT`
- perform one broader final differential audit across the implemented command registry
- keep future command additions wired through the authoritative metadata/key-extraction path

After that, the next roadmap target is common `CONFIG` compatibility, followed by ACL/authentication scope.