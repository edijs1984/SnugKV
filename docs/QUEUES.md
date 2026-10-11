# Job queues (`snugq_*`)

A BullMQ-style job queue, built into the server as a function library (`snugq`, loaded with the `snug` library at startup; `-builtin-functions=false` turns both off). The TypeScript client `snugkv-nest` wraps it as `Queue`, `Worker`, `QueueEvents` and `FlowProducer`; you can also call the functions directly with `FCALL`.

Every function takes the **queue prefix** as its only key, for example `snugq:{emails}:`. The braces keep all keys of one queue together. Job data, return values and progress are opaque strings: the server has no JSON in Lua, so clients serialise them.

## Features

- Jobs with data, FIFO, LIFO, priorities (1 is the most urgent, up to 2,097,152; no priority runs first)
- Delayed jobs, promote, change delay
- Attempts with fixed or exponential backoff, or a backoff the worker computes; unrecoverable failures skip the remaining attempts
- Locks with expiry, lock renewal and stalled-job recovery with a stall limit
- `removeOnComplete` / `removeOnFail` by count and age
- Pause and resume, global concurrency, queue rate limit (`max` jobs per `duration` ms) and a manual `rateLimit` from a worker
- Custom job ids (duplicates are ignored) and deduplication by key with an optional ttl
- Schedulers: repeat every N ms (server side) or by a pattern such as a cron expression (the client computes the next time)
- Parent/child flows with `waiting-children`, child results, and `fail`, `ignore` or `remove` on a child's failure
- Progress, logs, data updates, retry, remove, drain, clean, obliterate
- An events stream (`<prefix>events`) and per-minute completed/failed counters
- A queue registry (`snugq:queues`)

## Keys

| Key | Type | Holds |
| --- | --- | --- |
| `<prefix>job:<id>` | hash | the job (`name data ts delay prio att atm bt bd rmc rmca rmf rmfa pq pid onfail pend rv fr st prg po fo stc tk deid rep pat`) |
| `wait`, `paused`, `active` | list | job ids; the next job is taken from the right |
| `prioritized`, `delayed`, `completed`, `failed`, `waiting-children` | sorted set | job ids |
| `lock:<id>` | string | the worker token, with the lock's expiry |
| `meta` | hash | `paused`, `gconc` (global concurrency), `max` and `duration` (rate limit) |
| `repeat`, `repeat:<id>` | zset, hash | schedulers and the time of their next run |
| `job:<id>:logs`, `:processed`, `:failed`, `:deps` | list, hash, hash, set | logs, child results, failed children, unresolved children |
| `events` | stream | `added waiting delayed active completed failed retries stalled progress removed paused resumed duplicated deduplicated` |
| `wake` | pub/sub channel | a publish when work arrives, so workers need not poll |

## Functions

| Function | Arguments after the prefix | Returns |
| --- | --- | --- |
| `snugq_add` | `name data jobId delay priority lifo attempts backoffType backoffDelay rmcCount rmcAge rmfCount rmfAge parentPrefix parentId onFail waitChildren dedupId dedupTtl` | `{id, added}` |
| `snugq_fetch` | `token lockMs` | `{'job', id, {field, value...}}`, `{'empty', msUntilNextDelayed}`, `{'limited', ms}`, `{'paused'}`, `{'busy'}` |
| `snugq_complete` | `id token returnValue` | `0`, `-1` no such job, `-2` lock lost |
| `snugq_fail` | `id token reason stack final customBackoffMs` | `'retry'`, `'failed'`, `-1`, `-2` |
| `snugq_move_to_delayed` | `id token delayMs` | `0`, `-1`, `-2` |
| `snugq_move_to_wait` | `id token limitMs` | `0`, `-1`, `-2` (no attempt is used) |
| `snugq_extend_lock` | `id token ms` | `1` or `0` |
| `snugq_stalled` | `maxStalledCount intervalMs` | `{{stalled ids}, {failed ids}}`; runs once per interval across workers |
| `snugq_progress`, `snugq_update_data`, `snugq_log` | `id value [keepLogs]` | |
| `snugq_pause`, `snugq_resume` | | |
| `snugq_counts` | | `{type, count...}` |
| `snugq_jobs` | `type start end asc` | `{{id, {field, value...}}...}` |
| `snugq_remove` | `id` | `1`, `0`, `-1` locked; also removes unresolved children |
| `snugq_retry` | `id failed\|completed resetAttempts` | `0`, `-1`, `-5` |
| `snugq_promote`, `snugq_change_delay`, `snugq_change_priority` | `id ...` | `0`, `-1`, `-3` |
| `snugq_drain` | `includeDelayed` | removed count |
| `snugq_clean` | `type graceMs limit` | removed ids |
| `snugq_obliterate` | `force batch` | `1` call again, `0` done, `-1` active jobs |
| `snugq_upsert_scheduler` | `id every pattern nextMs limit endAt <19 job arguments>` | `1` when the first job was created |
| `snugq_scheduler_next`, `snugq_remove_scheduler` | `id nextMs` / `id` | |

`onFail` is empty (the parent keeps waiting), `fail` (the parent fails), `ignore` (the parent continues; the failure is recorded in `job:<parent>:failed`) or `remove` (the parent continues).

## Example

```
FCALL snugq_add 1 snugq:{emails}: welcome '{"to":"a@b.c"}' "" 0 0 0 3 exponential 1000 -1 0 -1 0 "" "" "" 0 "" 0
FCALL snugq_fetch 1 snugq:{emails}: worker-1 30000
FCALL snugq_complete 1 snugq:{emails}: 1 worker-1 '"sent"'
```

Rate limit and global concurrency are plain hash fields: `HSET snugq:{emails}:meta max 100 duration 1000 gconc 10`.

## Differences from BullMQ

- Data and results are strings, not JSON, on the server.
- Cron parsing and flow trees are built in the client.
- A stalled job goes back to the queue and keeps its attempt count.
