# Redis 8.2 command OOM differential audit

This audit covers command-level maxmemory admission behavior for the current single-node SnugKV surface under:

- `maxmemory-policy noeviction`
- the server already above `maxmemory`
- Redis 8.2 as the live oracle
- SnugKV on the current audit branch

Harness: `compat/oom/command-flags.sh`.

## Audited COMMAND flags

The following Redis 8.2 command flags were compared live and matched by SnugKV:

- `GET`: `readonly,fast`
- `SET`: `write,denyoom`
- `DEL`: `write`
- `EXPIRE`: `write,fast`
- `PERSIST`: `write,fast`
- `HSET`: `write,denyoom,fast`
- `HDEL`: `write,fast`
- `SADD`: `write,denyoom,fast`
- `SREM`: `write,fast`
- `LPUSH`: `write,denyoom,fast`
- `LPOP`: `write,fast`
- `ZADD`: `write,denyoom,fast`
- `ZREM`: `write,fast`
- `XADD`: `write,denyoom,fast`
- `XDEL`: `write,fast`
- `PFADD`: `write,denyoom,fast`
- `PFCOUNT`: `readonly`
- `SORT`: `write,denyoom,movablekeys`
- `SORT_RO`: `readonly,movablekeys`
- `COPY`: `write,denyoom`
- `RENAME`: `write`
- `TOUCH`: `readonly,fast`

## OOM behavior

With the server already above `maxmemory` and `noeviction` configured, Redis 8.2 establishes that admission follows the command's `denyoom` metadata rather than a generic write/read distinction or a prediction about whether the specific invocation will allocate.

Allowed while already OOM:

- `GET`
- `EXISTS`
- `TOUCH`
- `DEL`
- `EXPIRE`
- `PERSIST`
- `HDEL`
- `SREM`
- `LPOP`
- `ZREM`
- `XDEL`
- `PFCOUNT`
- `SORT_RO`
- `RENAME`

Rejected before execution while already OOM:

- `SET`, including replacement of an existing key
- `APPEND`
- `INCR`
- `HSET`
- `SADD`
- `LPUSH`
- `ZADD`
- `XADD`
- `PFADD`
- `SORT`, including `SORT` without `STORE`
- `COPY`

The Redis-compatible error is:

```
OOM command not allowed when used memory > 'maxmemory'.
```

SnugKV now emits the same text.

## Implementation notes

SnugKV centralizes the audited `denyoom` classification in command metadata and performs a noeviction admission check before executing denyoom commands.

Commands without `denyoom` must still be able to run while the store is already over the configured limit. Some SnugKV internal operations such as `LPOP` and `RENAME` can require temporary engine allocation even though their Redis command semantics permit execution in this state. The server therefore uses a scoped memory-admission bypass for ordinary non-denyoom write commands only.

The bypass deliberately excludes:

- `CONFIG` and other control-plane commands
- Lua scripting entry points
- Redis Function entry points

Scripting and Functions retain their own OOM admission semantics, including Function `allow-oom` / `no-writes` behavior.

## Validation

The final live differential matched Redis 8.2 for every audited command flag, return value, mutation outcome, and OOM error. The only remaining diff was the harness target banner:

- Redis: `target=redis82 addr=127.0.0.1:6390`
- SnugKV: `target=snugkv addr=127.0.0.1:6380`

Validation gates also passed:

- `go test -race -count=1 ./...`
- `go vet ./...`
- RESP command fuzzing for 10 seconds
- focused command-OOM regression tests

## Scope boundary

This closes command-level OOM admission parity for the audited `noeviction` surface.

Eviction-policy-specific behavior under `allkeys-lru` / `volatile-lru`, and Lua script flag behavior such as script-level `allow-oom`, remain separate follow-up work.
