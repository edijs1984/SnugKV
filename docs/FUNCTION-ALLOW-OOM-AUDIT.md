# Redis Functions allow-oom Differential Audit

This audit compares SnugKV against Redis 8.2 for Redis Function OOM-entry and
nested-command behavior.

## Harness

```text
compat/functions/allow-oom.sh
```

Validated against:

- SnugKV on port 6380
- Redis 8.2 on port 6390

## Redis 8.2 behavior reproduced by SnugKV

The following cases match semantically:

- a plain FCALL is rejected before execution when current used memory is already
  above maxmemory, even if the callback only performs a read;
- a plain FCALL write is also rejected before execution in the same state;
- a function registered with `allow-oom` may start while already above maxmemory;
- an `allow-oom` function may execute a memory-growing nested write;
- a function registered with `no-writes` may start while already above maxmemory;
- `no-writes` still forbids nested writes;
- `FCALL_RO` by itself does not bypass the OOM entry gate for an ordinary
  function;
- an `allow-oom` function may execute memory-reducing commands such as DEL;
- the allow-oom admission bypass is scoped to the Function invocation and is
  restored immediately after the call returns.

## SnugKV implementation

SnugKV records the `allow-oom` Function flag and exposes it through FUNCTION
LIST.

Before FCALL execution, SnugKV checks whether the current Function is allowed to
enter while over maxmemory. Functions with `allow-oom` or `no-writes` are
allowed; ordinary functions are rejected with the engine OOM error.

For `allow-oom`, the configured engine max-memory admission limit is
temporarily disabled only for the duration of the Function invocation and is
restored before FCALL returns. FCALL execution is serialized by the server's
durability mutex, so unrelated commands cannot consume that scoped bypass.

`no-writes` does not disable memory admission globally; it only permits the
Function to start while OOM because its nested command surface is read-only.

## Intentional formatting differences

Redis and SnugKV differ in error presentation:

- Redis 8.2: `OOM command not allowed when used memory > 'maxmemory'.`
- SnugKV: `OOM command not allowed when used memory exceeds max_memory`

For read-only Function write attempts, Redis reports its Function source
metadata while SnugKV reports the same underlying error through GopherLua's
runtime wrapper and stack trace.

These are formatting/runtime differences; the audited control flow, mutation
behavior, and memory-admission results match.
