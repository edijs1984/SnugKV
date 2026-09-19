# FUNCTION DUMP / RESTORE and restart persistence

SnugKV implements Redis-style Function library export/import commands:

```text
FUNCTION DUMP
FUNCTION RESTORE <payload> [APPEND|REPLACE|FLUSH]
```

`FUNCTION DUMP` serializes the currently loaded Function libraries. The payload is
binary-safe, versioned, and checksum-protected. `FUNCTION RESTORE` validates and
compiles the complete incoming payload before modifying the active registry, so a
corrupt payload or a compile/registration error leaves the existing registry
unchanged.

Restore policies follow Redis semantics:

- `APPEND` is the default. Existing library/function collisions abort the restore.
- `REPLACE` replaces libraries with matching library names, while still rejecting
  function-name collisions with unrelated libraries.
- `FLUSH` replaces the complete current registry with the payload contents.

## Redis RDB payload compatibility

SnugKV now uses the Redis 8.2 Function RDB payload format for `FUNCTION DUMP` and
`FUNCTION RESTORE`. Each library is encoded with `RDB_OPCODE_FUNCTION2` and the
original library source as an RDB string, followed by the Redis RDB version and CRC64
trailer. Redis-compatible integer/raw/LZF string decoding is supported, and SnugKV
emits Redis-compatible LZF compression when it saves at least four bytes.

Live cross-implementation validation covers both directions:

- Redis 8.2 `FUNCTION DUMP` -> SnugKV `FUNCTION RESTORE`;
- SnugKV `FUNCTION DUMP` -> Redis 8.2 `FUNCTION RESTORE`;
- successful `FCALL_RO` after each restore;
- byte-identical Redis/SnugKV payload output for the shared audited fixture;
- corrupt payload, checksum, version, truncation, restore-policy, and arity errors.

The empty-registry payload is also locked to the Redis 8.2 10-byte fixture
`0c0096ed6880f5553c93`.

## Restart durability

When SnugKV is started with either AOF or snapshot persistence configured, Function
library definitions are persisted to an atomic sidecar file:

```text
<aof-path>.functions
```

or, when AOF is not configured:

```text
<snapshot-path>.functions
```

After successful `FUNCTION LOAD`, `FUNCTION DELETE`, `FUNCTION FLUSH`, or
`FUNCTION RESTORE`, SnugKV writes a complete checksum-protected registry snapshot
to a temporary file, fsyncs it, atomically renames it into place, and fsyncs the
containing directory. On startup the sidecar is decoded and all libraries are
compiled before the registry is made available. Corrupt durable function state
causes startup recovery to fail rather than silently discarding libraries.

If neither AOF nor snapshot persistence is configured, the Function registry is
process-local and is lost on restart, matching the general expectation of running
an in-memory datastore without persistence enabled.

## Function-local Lua state

`FUNCTION DUMP` stores library source definitions, not live Lua VM-local variable
state. Restoring a library reconstructs its Lua state from source. For example, a
library-local counter initialized with `local n = 0` starts again from its source
initial value after `FUNCTION RESTORE` or process restart. This matches the model
that Function library definitions are durable while arbitrary VM execution state is
not part of the serialized library representation.

## Tests

Coverage includes:

- DUMP -> FLUSH -> RESTORE round trip;
- default APPEND collision rejection;
- REPLACE and FLUSH policies;
- invalid restore-policy handling;
- checksum/version/truncation corruption rejection with no registry mutation;
- Redis 8.2 LZF-compressed payload decoding and execution;
- Redis <-> SnugKV cross-restore in both directions;
- byte-identical Redis/SnugKV dump output for the audited fixture;
- function registry restoration across a simulated restart;
- persisted empty registry after `FUNCTION FLUSH`;
- rejection of corrupt durable function sidecar state at startup.

The repository CI gate remains:

```text
go test -race -count=1 ./...
go vet ./...
go test ./internal/resp -run '^$' -fuzz FuzzReadCommand -fuzztime=10s
```
