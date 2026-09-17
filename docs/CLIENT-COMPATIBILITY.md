# CLIENT compatibility

SnugKV implements the current single-node RESP2 CLIENT surface needed for connection identity, client metadata, connection introspection, targeted disconnects, and targeted unblocking of blocking commands.

## Implemented subcommands

- `CLIENT ID`
- `CLIENT GETNAME`
- `CLIENT SETNAME <name>`
- `CLIENT SETINFO LIB-NAME <value>`
- `CLIENT SETINFO LIB-VER <value>`
- `CLIENT INFO`
- `CLIENT LIST`
- `CLIENT LIST ID <id> [<id> ...]`
- `CLIENT LIST TYPE NORMAL`
- `CLIENT KILL ID <id> [SKIPME YES|NO]`
- `CLIENT UNBLOCK <id> [TIMEOUT|ERROR]`
- `CLIENT HELP`

## Registry model

CLIENT state is connection-scoped. The TCP server owns a concurrency-safe registry of active clients keyed by stable client ID. Each client record tracks the connection, local/remote address, name, library metadata, creation/last-command timestamps, last command, and blocking state.

The registry is initialized for both the main listener and the separate administration listener. Registration is defensive against partially constructed test servers so a nil registry cannot panic while the server mutex is held.

## Blocking integration

Blocking LIST/ZSET/STREAM commands already execute through cancellable waiter paths. CLIENT UNBLOCK adds a client-specific cancellation channel without closing the socket:

- `CLIENT UNBLOCK <id> TIMEOUT` returns the same null-style blocking reply as a normal timeout.
- `CLIENT UNBLOCK <id> ERROR` returns `UNBLOCKED client unblocked via CLIENT UNBLOCK`.
- In both cases the target connection remains usable after the blocked command returns.

`CLIENT KILL ID` closes only the selected connection. The default `SKIPME YES` behavior prevents a client from killing itself; `SKIPME NO` allows self-kill and returns the command reply before the connection closes.

## Redis differential validation

Live Redis 6379 vs SnugKV 6380 testing covered multiple simultaneous persistent connections and matched the intended semantics for:

- positive, distinct client IDs;
- SETNAME/GETNAME connection-local state;
- SETINFO library metadata;
- CLIENT INFO bulk-string output and core field presence;
- CLIENT LIST visibility of multiple live clients;
- CLIENT LIST ID filtering;
- CLIENT LIST TYPE NORMAL filtering;
- CLIENT KILL ID return value and connection closure;
- default self-kill SKIPME behavior;
- explicit `SKIPME NO` self-kill behavior;
- CLIENT UNBLOCK TIMEOUT reply semantics and connection survival;
- CLIENT UNBLOCK ERROR reply semantics and connection survival;
- nonexistent KILL/UNBLOCK IDs returning integer 0;
- invalid UNBLOCK ID/reason errors;
- tested wrong-arity error wording.

CLIENT INFO/LIST runtime values such as file descriptor, qbuf/rbuf allocation, and Redis internal memory counters are not expected to be numerically identical. SnugKV reports the Redis-shaped core fields needed for client/tooling compatibility while fields tied to Redis internals may use neutral values.

## Validation gates

The CLIENT registry work passed:

```text
go test -race -count=1 ./internal/server
go test -race -count=1 ./...
go vet ./...
go test ./internal/resp -run '^$' -fuzz FuzzReadCommand -fuzztime=10s
go build ./cmd/snugkv
```

Focused tests also cover the separate admin listener, registry lifecycle, INFO/LIST, KILL, UNBLOCK state transitions, and blocking timeout response shapes.

## Current boundary

The implemented `CLIENT LIST TYPE` surface currently supports `NORMAL`, matching SnugKV's tracked client class. Redis client classes tied to replication or other unsupported topology features are intentionally not advertised as implemented. Tracking/caching/redirection CLIENT features are also outside the current RESP2 single-node compatibility milestone.
