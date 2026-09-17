# Scripting and Redis Functions Differential Audit

Date: 2026-09-17

This records a live Redis 6379 versus SnugKV 6380 comparison for the read-only Lua scripting and Redis Functions core added in PR #82, plus the follow-up FUNCTION LIST wire fix in PR #83.

## Read-only scripting

The live comparison confirmed:

- `EVAL_RO` can read keys normally.
- `EVAL_RO` rejects write commands and leaves the target key unchanged.
- `redis.pcall()` from `EVAL_RO` returns `ERR Write commands are not allowed from read-only scripts.` without mutating data.
- `SCRIPT LOAD` produced the same SHA-1 on Redis and SnugKV for the tested script.
- `EVALSHA_RO` successfully executed the shared cached script.

Redis and SnugKV differ in direct `redis.call()` runtime stack formatting because SnugKV uses GopherLua. The observable command error and mutation semantics matched.

## Redis Functions core

The live comparison confirmed:

- `FUNCTION LOAD` returns the library name.
- `FCALL` passes KEYS/ARGV and can mutate/read data.
- `FCALL_RO` executes `no-writes` functions and rejects writes.
- The `no-writes` flag also blocks writes when the same function is invoked through ordinary `FCALL`.
- Library-local Lua state persists across FCALL invocations (`1`, `2`, `3` counter sequence matched Redis).
- `FUNCTION LIST` metadata structure, `LIBRARYNAME` filtering, and `WITHCODE` content matched semantically.
- `FUNCTION LOAD` without `REPLACE` rejects an existing library with `ERR Library 'testlib' already exists`.
- `FUNCTION LOAD REPLACE` replaces the old function set.
- Removed/replaced functions return `ERR Function not found`.
- `FUNCTION DELETE` and `FUNCTION FLUSH` matched Redis behavior.

## FUNCTION LIST flag wire fix

The initial live audit exposed one RESP2 wire-type mismatch: Redis emitted function flags such as `no-writes` as RESP simple strings, while SnugKV emitted them as bulk strings. That caused redis-cli to render Redis as `no-writes` but SnugKV as `"no-writes"`.

PR #83 changed SnugKV to emit the flag as a RESP simple string and added an exact regression test. A follow-up live check confirmed redis-cli now renders:

```text
"flags"
1) no-writes
```

matching Redis.

## Intentional / documented differences

- `FUNCTION LIST` library ordering is not treated as a compatibility contract.
- Direct Lua runtime stack/error trace text is VM-specific; SnugKV uses GopherLua rather than Redis's Lua runtime.
- Loaded Function libraries are process-local in this milestone and are not yet persisted/restored across SnugKV restart.
- `FUNCTION DUMP`, `FUNCTION RESTORE`, `FUNCTION STATS`, `FUNCTION KILL`, `FUNCTION HELP`, `SCRIPT KILL`, and `SCRIPT DEBUG` remain outside this completed core slice.

## Result

The implemented `EVAL_RO` / `EVALSHA_RO` and Redis Functions core (`FUNCTION LOAD/LIST/DELETE/FLUSH`, `FCALL`, `FCALL_RO`, `no-writes`) passed the live Redis differential audit for the tested common surface. The only discovered wire mismatch was fixed in PR #83 and manually reverified.