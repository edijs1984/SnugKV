# Atomic transactions (`MULTI ATOMIC`)

Plain `MULTI`/`EXEC` behaves exactly as in Redis: queued commands run back to back
with nothing interleaved, but a command that fails at run time does not undo the
ones before it.

`MULTI ATOMIC` makes the transaction all-or-nothing:

```
MULTI ATOMIC
SET balance:alice 50
DECRBY balance:bob 50
EXEC
```

If every command succeeds, `EXEC` returns the usual array of replies. If any
command fails (wrong type, out of memory, bad argument, ...), every earlier write
in the transaction is undone and `EXEC` returns one error:

```
-EXECABORT Atomic transaction rolled back: command 2 (DECRBY) failed: ERR value is not an integer or out of range
```

## Making everything atomic

Start the server with `-atomic-transactions` (or `SNUGKV_ATOMIC_TRANSACTIONS=true`,
or `"atomic_transactions": true` in the config file) and every plain `MULTI`/`EXEC`
is atomic, so existing clients and libraries (for example Redis client pipelines
that use transactions) get all-or-nothing behaviour without code changes. Every
writable `EVAL` and `FCALL` is atomic as well. Commands listed under "Not allowed"
are then rejected inside every `MULTI`.

## Atomic scripts and functions

A script or function that fails (a Lua error, or a failing `redis.call`) normally
keeps the writes it made before the failure, as in Redis. To roll them back:

```
EVAL "#!lua flags=atomic
redis.call('SET', KEYS[1], 'x')
redis.call('LPUSH', KEYS[1], 'y')" 1 mykey
```

or register the function with the flag:

```lua
redis.register_function{function_name='transfer', callback=transfer, flags={'atomic'}}
```

A failed atomic script or function leaves the keyspace as it was and nothing reaches
the append-only file. The snapshot covers the whole keyspace, so use it for scripts
that matter more than raw speed on a very large database.

## ACID properties

| | |
|---|---|
| Atomicity | Rolled back as a whole on any failure. The keys a transaction can change are snapshotted before it runs (a transaction containing a script, `FLUSHALL`/`FLUSHDB` or `SORT` snapshots the whole database instead) and restored exactly: value, type and expiry. |
| Consistency | Commands keep their own invariants. A rolled-back transaction leaves the database in the state it had before. |
| Isolation | `EXEC` runs under the same exclusive lock as scripts, so no other client sees any intermediate state, including state that is later rolled back. `WATCH` works as before. |
| Durability | Only a committed transaction is written to the append-only file, as one frame, and sent to replicas. With `appendfsync always` it is on disk before the reply. A rolled-back transaction leaves nothing in the log. If the log write fails the transaction is rolled back and the server refuses further writes until storage is repaired. |

## Not allowed inside `MULTI ATOMIC`

Commands whose effect cannot be undone are rejected when they are queued, and the
transaction then fails with `EXECABORT`: `PUBLISH`, `SPUBLISH`, `WAIT`, `WAITAOF`,
`CONFIG`, `ACL`, `CLIENT`, `DEBUG`, `FUNCTION`, `SCRIPT`, `SHUTDOWN`, `REPLICAOF`,
`SAVE`/`BGSAVE`/`BGREWRITEAOF`, `MIGRATE`, `CLUSTER`, `SWAPDB`, `MONITOR`, `AUTH`,
`HELLO`, index-definition `FT.*` commands, and the admin `SNUG.*` commands.
A script run with `EVAL` inside the transaction may still call `PUBLISH`; that
message is delivered immediately and is not rolled back.

Blocked clients (`BLPOP` and similar) are woken only after the transaction commits.
Client-side caching invalidation messages sent for a rolled-back write are not
retracted; they only cause an extra refetch.

## Cost

Atomic transactions export the prior value of the touched keys once and, when an
append-only file or replicas are in use, the new value once. Transactions with
scripts or `FLUSH*` copy the whole keyspace, which is slow on large databases.
