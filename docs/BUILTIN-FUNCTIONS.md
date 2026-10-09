# Built-in functions (`snug_*`)

SnugKV loads a small function library named `snug` at startup, so common patterns that need an atomic read-modify-write are one `FCALL` away. Every function runs as a single isolated step. Turn the library off with `-builtin-functions=false`.

`FCALL snug_functions_version 0` returns the library version. `FUNCTION FLUSH` removes the library until the next restart. Under cluster mode, pass keys that share one hash tag.

| Function | Call | Returns |
|---|---|---|
| `snug_rate_limit` | `FCALL snug_rate_limit 1 <bucket> <capacity> <refill_per_second> [cost]` | `{allowed 1/0, tokens left, ms until cost is available}` |
| `snug_lock_acquire` | `FCALL snug_lock_acquire 2 <lock> <fence-counter> <owner> <ttl_ms>` | fencing token (grows with every new holder), `0` if held by another owner |
| `snug_lock_release` | `FCALL snug_lock_release 1 <lock> <owner>` | `1` released, `0` not the holder |
| `snug_lock_renew` | `FCALL snug_lock_renew 1 <lock> <owner> <ttl_ms>` | `1` extended, `0` not the holder |
| `snug_idem_begin` | `FCALL snug_idem_begin 1 <key> <ttl_ms>` | `{1}` go ahead, `{0,'pending'}`, or `{0,'done',<result>}` |
| `snug_idem_commit` | `FCALL snug_idem_commit 1 <key> <result> <ttl_ms>` | `1` stored, `0` not pending |
| `snug_idem_abort` | `FCALL snug_idem_abort 1 <key>` | `1` released, `0` not pending |
| `snug_counter_add` | `FCALL snug_counter_add 1 <key> <delta> <min> <max>` | `{applied 1/0, value}`; applies only if the result stays in `[min, max]` |
| `snug_queue_push` | `FCALL snug_queue_push 3 <ready> <inflight> <payloads> <payload>` | message id |
| `snug_queue_pop` | `FCALL snug_queue_pop 3 <ready> <inflight> <payloads> <visibility_ms>` | `{id, payload}` or nil; unacknowledged messages are redelivered after `visibility_ms` |
| `snug_queue_ack` | same keys, `<id>` | `1` acknowledged, `0` unknown |
| `snug_queue_nack` | same keys, `<id>` | `1` requeued, `0` unknown |
| `snug_leaderboard_submit` | `FCALL snug_leaderboard_submit 1 <board> <member> <score> [max\|min\|replace]` | `{rank (0 is first), score}`; default keeps the better score |
| `snug_leaderboard_around` | `FCALL_RO snug_leaderboard_around 1 <board> <member> <radius>` | `{rank, score, {member, score, ...}}` window, best first; rank is nil if absent |

Time comes from the server clock, so callers cannot skew it. The functions are plain Lua and can be read with `FUNCTION LIST WITHCODE`.
