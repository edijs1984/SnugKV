# SCRIPT DEBUG compatibility

SnugKV implements a deliberately scoped Redis-compatible Lua debugging foundation.

## Implemented

Connection-scoped:

- `SCRIPT DEBUG NO`
- `SCRIPT DEBUG YES`
- `SCRIPT DEBUG SYNC`

The next `EVAL` on that connection enters a Redis-shaped LDB session.

Supported debugger command:

- `C` / `CONTINUE`

### YES

`SCRIPT DEBUG YES` executes the debugged script against a temporary logical clone
of the current keyspace.

Observable behavior:

- the initial LDB stop frame matches Redis for the audited path;
- `CONTINUE` returns the Redis-shaped `<endsession>` frame plus the script result;
- dataset writes performed by the script are discarded when the session ends.

The clone is built from the engine's logical `Export(nil)` / `Restore(..., true)`
path, so native datatypes and absolute TTLs are preserved in the debug copy and no
AOF/journal mutations are emitted from the async debugger execution.

### SYNC

`SCRIPT DEBUG SYNC` executes the debugged script against the real SnugKV server.

Observable behavior:

- the initial stop frame matches Redis for the audited path;
- `CONTINUE` returns the Redis-shaped `<endsession>` frame plus the script result;
- dataset writes remain visible after the session, matching Redis SYNC behavior.

The invoking connection's ACL execution context is retained for the debugged script.

## Redis 8.2 differential evidence

Raw RESP differential coverage is in:

```
compat/scripting/script-debug-wire.py
```

For the audited `YES` and `SYNC` continue-only sessions, Redis 8.2 and SnugKV
produce the same RESP frames. The only textual difference in the saved oracle
output is the target port printed by the harness.

Command-level syntax/error coverage is in:

```
compat/scripting/script-debug.sh
```

Redis 8.2 behavior confirmed:

- `SCRIPT DEBUG NO|YES|SYNC` => `OK`
- invalid mode => `ERR Use SCRIPT DEBUG YES/SYNC/NO`
- missing/extra arguments => Redis-style wrong-arity error
- YES rolls dataset writes back
- SYNC retains dataset writes

## Not implemented

Full Redis LDB stepping remains intentionally incomplete:

- `S` / step
- `N` / next
- `B` / breakpoints
- `L` / source listing
- `T` / stack/backtrace
- variable inspection
- `redis.debug()` streaming
- `redis.breakpoint()`

Redis 8.2 wire behavior for these commands is captured in:

```
compat/scripting/script-debug-commands-wire.py
```

The current embedded Lua runtime is GopherLua v1.1.2. GopherLua does not implement
Lua debug hooks, which are the mechanism required for correct line-by-line
suspension during an already-running script. SnugKV therefore does not fake
stepping by merely walking source text.

Completing full LDB semantics requires either:

1. a Lua VM with usable line/instruction debug hooks; or
2. a maintained GopherLua fork that adds equivalent hook support.

Until then, SnugKV advertises the real continue-only LDB foundation rather than
claiming full Redis debugger parity.


## Full LDB Redis 8.2 oracle

The full debugger command protocol has now been audited against Redis 8.2 with:

```
compat/scripting/script-debug-ldb-wire.py
```

The oracle covers step, next, breakpoint management, source listing, stack trace,
local-variable inspection, `redis.debug()`, `redis.breakpoint()`, invalid
debugger commands, and end-session framing.

Implementation details and the controlled GopherLua v1.1.2 hook-fork design are
recorded in:

```
docs/SCRIPT-DEBUG-LDB-IMPLEMENTATION.md
```

A historical 2019 hook fork was inspected only as design evidence and is not a
suitable runtime dependency for SnugKV.
