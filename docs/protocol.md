# RESP2 protocol

TCP requests are nonempty flat RESP2 arrays of non-null bulk strings. Binary and
empty values, partial TCP frames, and pipelined commands are supported. Inline
commands, nested arrays, null arguments, simple strings, integers, and error frames
are rejected as requests.

Default limits are 64 MiB per command, 32 MiB per bulk string, 1,024 arguments,
10,000 concurrent clients, and 30 seconds per complete read or write. They are
configured before startup. Payload storage grows as bytes arrive after validating
the declared length. Responses are written synchronously, so there is no unbounded
output queue; partial writes are retried.

Malformed protocol receives `-ERR invalid RESP` and the connection closes. Command
errors preserve the connection. Error framing strips carriage returns and newlines.
`QUIT` returns `+OK` and closes the connection.

## Command execution and atomicity

Commands run sequentially per connection. Per-key mutations are atomic. Multi-key
operations that require a consistent view lock participating shards in a stable
order or use a dedicated all-shard/cross-key primitive. This includes string
multi-key operations, SET algebra/store, LIST moves, ZSET algebra/store, ZMPOP, and
ZRANGESTORE destination/source handling.

Write commands with AOF enabled execute inside the durability protocol: snapshot
affected logical keys, apply the in-memory mutation, append logical post-state, and
roll back if the append fails. Commands with dynamic key sets such as `ZMPOP` derive
the affected key set from command arguments before mutation.

## Blocking commands

`BLPOP`, `BRPOP`, `BLMOVE`, and `BRPOPLPUSH` use a waiter/wakeup registry rather
than polling. A blocking wait does not hold the AOF durability mutex. Once a key is
signaled, the actual pop/move is retried through the normal durable mutation path.
This avoids a sleeping client stalling unrelated durable writes and closes the
register/check/sleep lost-wakeup race.

A timeout of `0` means an infinite wait. Server shutdown cancels infinite LIST
waiters. Proactive cancellation when an infinitely blocked client disconnects is a
remaining hardening item. Blocking ZSET commands are not implemented yet.

## RESP version

`HELLO 2` is supported. RESP3 is not implemented and `HELLO 3` is rejected with
`NOPROTO unsupported protocol version`.

## Administration

Administrative commands use the separate loopback admin listener when configured.
That listener rejects data mutations; the public listener rejects `SNUG.*` while
the admin listener is active.
