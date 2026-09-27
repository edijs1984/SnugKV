# MONITOR Audit

Date: 2026-09-27

## Scope

This audit covers SnugKV's Redis-style `MONITOR` implementation on the ordinary
TCP listener.

Implemented and covered behavior:

- `MONITOR` enters streaming mode after `+OK`.
- Events include a Unix timestamp with microsecond precision, logical DB `0`,
  source/client address, and quoted command arguments.
- Binary arguments use Redis-style escaped rendering for quotes, backslashes,
  control bytes, and non-printable bytes.
- `MULTI` / queued commands / `EXEC` preserve execution-order visibility.
- `RESET` leaves MONITOR mode and allows the same connection to subscribe again.
- `QUIT` returns `+OK` and closes the connection.
- ACL authorization is enforced for `MONITOR`.
- Nested Lua and Redis Function commands use source `lua`.
- Outer script/function invocations are emitted before nested Lua calls.
- Covered script families: `EVAL`, `EVALSHA`, `EVAL_RO`, `FCALL`,
  and `FCALL_RO`.

## Sensitive-command policy

SnugKV does not emit credential-bearing or administrative commands into the
MONITOR stream.

Explicitly suppressed:

- `AUTH`
- `HELLO`
- `ACL`
- `CONFIG`
- `MIGRATE`
- `MONITOR`

Commands classified in the Redis `@admin` ACL category are also suppressed.

This is an intentional safety boundary: MONITOR must not become a credential or
administrative-secret disclosure channel.

## Nested scripting and Functions

MONITOR integration exists in every active nested execution bridge used by the
current runtime:

- legacy Lua bridge
- killable Lua bridge
- read-only Lua bridge
- killable writable Function bridge

This matters because SnugKV has separate execution paths for ordinary scripting,
read-only scripting, and killable Function execution. The focused MONITOR tests
caught and fixed missing observability in the killable EVAL and writable FCALL
paths.

For an invocation such as:

```
EVAL "redis.call('SET',KEYS[1],ARGV[1]); return redis.call('GET',KEYS[1])" 1 monitor:lua hello
```

the expected logical order is:

1. outer `EVAL`
2. `[0 lua] "SET" "monitor:lua" "hello"`
3. `[0 lua] "GET" "monitor:lua"`

Inside `MULTI` / `EXEC`, the corresponding order is:

1. `MULTI`
2. outer script/function invocation
3. nested Lua commands
4. `EXEC`

## Backpressure and lifecycle

Each MONITOR subscriber has a bounded event queue. A subscriber that cannot keep
up is disconnected rather than being allowed to grow memory without bound.

Removal waits for an in-flight delivery before RESET completes, preventing an old
MONITOR event from appearing after the RESET reply.

Individual events are also bounded; oversized command payloads cause monitor
subscribers to be disconnected instead of allocating an unbounded event.

## Focused verification

The focused race-enabled gate is:

```bash
go test -race ./internal/server -run '^TestMonitor' -count=1 -v
```

Covered tests include:

- ordinary command and transaction events
- binary escaping
- sensitive command suppression
- ACL denial
- RESET / QUIT lifecycle
- repeated MONITOR -> RESET -> MONITOR cycles
- direct and EXEC Lua ordering
- `EVALSHA`
- `EVAL_RO`
- `FCALL`
- `FCALL_RO`

The operator-reported focused race suite passed after the nested Function path
fixes.

## Remaining validation before merge

Run the normal repository-wide gates:

```bash
go test -race ./...
go vet ./...
go test ./internal/resp -run=^$ -fuzz=FuzzDecode -fuzztime=20s
```

After those pass, the MONITOR Phase F slice is ready for PR/CI review.
