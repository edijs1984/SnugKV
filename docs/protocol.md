# RESP protocol

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

LIST blocking commands (`BLPOP`, `BRPOP`, `BLMOVE`, `BRPOPLPUSH`) and ZSET
blocking commands (`BZPOPMIN`, `BZPOPMAX`, `BZMPOP`) use per-key waiter/wakeup
registries rather than polling.

The waiter is registered before the read-only readiness check. This closes the
register/check/sleep lost-wakeup race: a producer either makes the value visible to
the readiness check or closes the waiter's channel after the check. Multiple
waiters can wake on the same write and race through the normal atomic mutation
path; losers re-register and continue waiting.

A sleeping blocker does not hold the AOF durability mutex. Once a relevant key is
signaled, the actual `LPOP`/`RPOP`/`LMOVE`, `ZPOPMIN`/`ZPOPMAX`, or `ZMPOP`
operation is executed through the same durable path as the corresponding
non-blocking command. Empty readiness checks are read-only and do not append no-op
AOF state.

Blocking ZSET commands preserve Redis RESP2 reply shapes: `BZPOPMIN`/`BZPOPMAX`
return `[key, member, score]`; `BZMPOP` returns `[key, [[member, score], ...]]`.
A timeout produces a nil array reply. Timeouts accept fractional seconds and `0`
means an infinite wait.

Server shutdown cancels infinite LIST and ZSET waiters. Proactive cancellation
when an infinitely blocked client disconnects is a remaining hardening item.

## RESP version

RESP2 remains the default wire protocol. `HELLO 3` negotiates RESP3 and
`HELLO 2` switches a connection back to RESP2. The implemented RESP3 surface
includes the null/map/set/double/verbatim reply forms used by current commands,
nested COMMAND/ACL shapes, and Pub/Sub push frames. RESP2 behavior remains
regression-covered; see `RESP3-COMPATIBILITY.md` for the audited boundaries.

## Cluster routing

In cluster mode SnugKV uses Redis-compatible 16,384 hash slots and hash tags.
Requests can receive `MOVED`, `ASK`, `CROSSSLOT`, or
`CLUSTERDOWN Hash slot not served` according to the current topology.
`ASKING` is connection-scoped and one-shot.

Peer-only distributed control uses a separate connection-scoped internal identity
established with `SNUG.INTERNAL AUTH`; it is not equivalent to normal ACL admin
access and is revoked by `RESET`, `AUTH`, or `HELLO`.

## Administration

Administrative commands use the separate loopback admin listener when configured.
That listener rejects data mutations; the public listener rejects `SNUG.*` while
the admin listener is active.
