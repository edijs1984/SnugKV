# Redis 8.2 Lua OOM flags differential audit

This audit covers Redis 8.2 Eval-script OOM admission and shebang flag behavior for the current single-node SnugKV scripting surface.

Harness: `compat/scripting/lua-oom-flags.sh`.

## Redis 8.2 oracle

The live Redis 8.2 oracle established the following behavior while the server is already above `maxmemory` with `maxmemory-policy noeviction`.

### Legacy Eval scripts

Scripts without a shebang retain Redis's legacy OOM behavior.

- A read-only script can run while already OOM.
- A first nested command carrying `denyoom` is rejected.
- If an earlier nested write that is permitted while OOM succeeds, later writes in the same script invocation are admitted.

Observed example:

```lua
redis.call('DEL','oom:delete')
return redis.call('SET','oom:new','x')
```

The `DEL` succeeds while OOM and the later `SET` is admitted for the remainder of that script invocation.

### Shebang scripts

Scripts beginning with `#!lua` use invocation-level policy.

- A shebang script with no flags is rejected at invocation while already OOM.
- `flags=allow-oom` permits invocation while already OOM and permits nested memory-growing writes.
- `flags=no-writes` permits invocation while already OOM and enforces read-only execution.
- `EVAL_RO` / `EVALSHA_RO` reject a shebang script that is write-capable.
- Unknown shebang flags are rejected.
- `SCRIPT LOAD` preserves the original source and SHA; `EVALSHA` applies the same shebang metadata and OOM policy as `EVAL`.

Supported standalone-safe Eval flags parsed by SnugKV:

- `no-writes`
- `allow-oom`
- `allow-stale`
- `no-cluster`
- `allow-cross-slot-keys`

Only flags relevant to current single-node behavior change execution semantics today. The remaining standalone-safe flags are accepted for Redis-compatible metadata parsing.

## SHA and compilation behavior

SnugKV hashes and caches the original script source, including the shebang line, matching Redis SHA identity.

Before Lua compilation, the shebang metadata line is removed and only the Lua body is passed to GopherLua.

This behavior applies consistently to:

- `EVAL`
- `EVALSHA`
- `EVAL_RO`
- `EVALSHA_RO`
- `SCRIPT LOAD`
- the killable scripting execution path used by top-level Eval commands

## OOM admission implementation

For flagged scripts:

- default write-capable shebang scripts are rejected at invocation if already OOM;
- `allow-oom` bypasses that entry gate and temporarily disables engine max-memory admission for the invocation;
- `no-writes` permits OOM entry but routes nested commands through the read-only script bridge;
- the original max-memory setting is always restored before the invocation returns.

For legacy scripts:

- the script starts with ordinary command-level OOM admission;
- once the first successful write command is admitted while already OOM, memory admission is bypassed for subsequent writes in that same script invocation;
- the bypass is scoped to that invocation and the configured max-memory limit is restored afterward.

## Differential result

The final SnugKV vs Redis 8.2 live differential matched all audited semantic outcomes:

- legacy reads
- first-write OOM rejection
- legacy shrinking-write then growing-write behavior
- default shebang OOM-entry rejection
- `allow-oom`
- `no-writes`
- `EVAL_RO`
- unknown flag rejection
- `SCRIPT LOAD`
- `EVALSHA`

The remaining differences are Lua runtime error formatting only.

Redis formats script runtime failures using its own Lua source metadata, for example:

```
... script: <sha>, on @user_script:<line>.
```

SnugKV uses GopherLua's native runtime/source formatting, which includes `<string>` and a GopherLua stack traceback.

SnugKV intentionally does not fabricate Redis LDB/source metadata solely to hide the runtime implementation difference.

## Validation

The milestone passed:

- focused Lua OOM/shebang regression tests
- live Redis 8.2 differential harness
- `go test -race -count=1 ./...`
- `go vet ./...`
- RESP command fuzzing for 10 seconds

## Scope boundary

This closes the audited Lua shebang/OOM-flag parity work for the current `noeviction` surface.

Still separate:

- `SCRIPT DEBUG` / Redis LDB semantics
- eviction-policy-specific scripting behavior under policies such as `allkeys-lru` and `volatile-lru`
- Redis-RDB byte compatibility for Function DUMP/RESTORE
