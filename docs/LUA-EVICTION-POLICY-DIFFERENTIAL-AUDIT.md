# Redis 8.2 Lua eviction-policy differential audit

This audit covers Lua scripting behavior under SnugKV's implemented LRU eviction policies:

- `allkeys-lru`
- `volatile-lru`

It complements the separate `noeviction` Lua OOM/shebang audit in
`docs/LUA-OOM-FLAGS-DIFFERENTIAL-AUDIT.md`.

Harness:

```
compat/scripting/lua-eviction-policies.sh
```

## Oracle setup

The same semantic harness was run against:

- Redis 8.2 on port 6390
- SnugKV on port 6380

The harness uses deterministic high-entropy payloads rather than repeated-byte
values so SnugKV compression does not distort the intended memory-pressure test.

It also reports semantic invariants instead of exact surviving key names because
sampled LRU implementations may legitimately select different eligible victims.

## Redis 8.2 behavior

### allkeys-lru

Under `allkeys-lru`:

- legacy EVAL scripts can trigger eviction and complete a memory-growing write;
- default `#!lua` scripts can trigger eviction and complete the write;
- `flags=allow-oom` still participates in normal eviction when victims exist;
- `flags=no-writes` performs read-only execution and does not spuriously evict keys.

The audited invariant is that a memory-growing write succeeds and at least one
eligible key can be evicted.

### volatile-lru

Under `volatile-lru`:

- legacy scripts can trigger eviction and complete a growing write when expiring
  keys are available;
- default `#!lua` scripts behave the same way;
- `allow-oom` still permits the normal volatile eviction path first;
- persistent keys are not selected as volatile eviction victims.

When no expiring victim exists:

- a normal legacy script receives OOM;
- `flags=allow-oom` can still complete the nested memory-growing write;
- persistent keys remain present.

## SnugKV implementation

The scripting memory-admission path is policy-aware.

### noeviction

The existing invocation-level semantics remain unchanged:

- flagged scripts use Redis-style invocation admission;
- `allow-oom` can bypass memory admission for the invocation;
- legacy first-write admission behavior remains intact.

### eviction policies

For `allkeys-lru` and `volatile-lru`:

1. the nested command runs through the normal pressure path;
2. SnugKV performs cleanup/compaction;
3. eligible victims are selected according to the configured policy;
4. the command is retried after eviction;
5. only if eviction is exhausted and the script carries `allow-oom`, that one
   nested command is retried with engine max-memory admission temporarily disabled.

This ordering is important: `allow-oom` must not suppress ordinary eviction.

For `volatile-lru`, persistent keys remain ineligible as victims.

## Differential result

The final live Redis 8.2 vs SnugKV differential matched all audited semantic
outcomes:

- allkeys-lru legacy script eviction;
- allkeys-lru default shebang eviction;
- allkeys-lru `allow-oom` with eviction;
- allkeys-lru `no-writes` read-only behavior;
- volatile-lru legacy eviction;
- volatile-lru default shebang eviction;
- volatile-lru `allow-oom` with eviction;
- persistent-key preservation under volatile-lru;
- volatile-lru OOM when no eligible victim exists;
- volatile-lru `allow-oom` success when no eligible victim exists.

The remaining textual difference is Lua runtime error formatting only.

Redis reports script source metadata in its own form:

```
... script: <sha>, on @user_script:<line>.
```

SnugKV uses GopherLua's native runtime/source traceback formatting.

This is the same intentional implementation-specific diagnostic difference
documented by the noeviction Lua OOM audit.

## Test design notes

The first version of the harness used repeated-byte payloads such as `NNNN...`.
That was rejected because SnugKV can compress such values, making nominal payload
sizes incomparable to Redis memory pressure.

The final harness uses deterministic high-entropy ASCII payloads.

The harness also avoids asserting which exact LRU victim survives because Redis
and SnugKV use independent sampling/internal layouts. It checks policy-level
invariants instead.

## Validation

Focused regression coverage includes:

- allkeys-lru script eviction;
- volatile-lru TTL-victim eviction;
- persistent-key preservation;
- legacy OOM when no volatile victim exists;
- `allow-oom` fallback when no volatile victim exists;
- regression coverage for the previously completed noeviction Lua OOM semantics.

Final release gates for the branch are:

- `go test -race -count=1 ./...`
- `go vet ./...`
- RESP parser fuzzing

## Scope boundary

This closes the current Redis 8.2 differential target for Lua scripting under
SnugKV's implemented `allkeys-lru` and `volatile-lru` policies.

Still separate:

- `SCRIPT DEBUG` / Redis LDB semantics;
- optional Function DUMP/RESTORE Redis-RDB byte compatibility;
- additional eviction policies if SnugKV implements them in the future.
