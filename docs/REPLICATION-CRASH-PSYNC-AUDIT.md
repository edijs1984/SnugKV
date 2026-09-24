# Replication Crash-Safe PSYNC Audit

Date: 2026-09-24

## Scope

SnugKV now persists Redis replica continuation metadata in the same checksummed AOF frame as the replicated logical mutation.

This couples:

- the recovered keyspace state
- the upstream replication ID
- the exact safe Redis replication offset
- Redis replication-stream mode

so an unclean process crash cannot recover an offset that is ahead of the recovered dataset.

## Durability model

For an ordinary Redis replication batch, SnugKV:

1. applies the batch under the durability lock
2. derives the logical mutations
3. appends those mutations and the safe upstream offset in one AOF frame
4. advances the in-memory replication offset only after the append succeeds

For `MULTI/EXEC`, the checkpoint offset is the byte offset after the complete transaction.

If the AOF append fails, the logical mutation is rolled back and the in-memory offset is not advanced.

## FULLRESYNC safety

A Redis full synchronization persists:

1. an explicit replication continuation tombstone containing the intended upstream host/port
2. a keyspace reset
3. the incoming full dataset
4. a new safe replication checkpoint

If SnugKV crashes during full-sync replacement before the final checkpoint, recovery retains the upstream target but not the stale replid/offset. SnugKV therefore requests a fresh FULLRESYNC instead of exposing a partial recovered dataset as standalone state.

## Topology changes

`REPLICAOF NO ONE` durably invalidates prior continuation state.

Switching to another upstream also writes a durable tombstone before beginning the new follow operation.

## Live SIGKILL validation

Topology:

- Redis primary: 127.0.0.1:6398
- SnugKV replica: 127.0.0.1:6393
- AOF: /tmp/snugkv-crash.aof
- appendfsync: always

Replicated data before crash:

- crash:a = 101
- crash:b = 200

The SnugKV replica process was terminated with `SIGKILL`, so no graceful shutdown handler or sidecar checkpoint could run.

After restarting from the same AOF:

- role remained replica
- master_link_status returned up
- crash:a recovered as 101
- crash:b recovered as 200
- a new post-restart write replicated successfully

A second SIGKILL/restart was traced directly on the Redis primary. Redis logged:

```
Connection with replica 127.0.0.1:<unknown-replica-port> lost.
Replica 127.0.0.1:<unknown-replica-port> asks for synchronization
Partial resynchronization request from 127.0.0.1:<unknown-replica-port> accepted. Sending 686 bytes of backlog starting from offset 7680.
```

This is direct evidence that SnugKV recovered its durable Redis continuation tuple after an unclean process death and resumed with PSYNC +CONTINUE rather than FULLRESYNC.

## Compatibility boundary

Crash-safe PSYNC requires AOF persistence because the safe upstream offset is coupled to replicated mutations in AOF frames.

With `appendfsync always`, every acknowledged persisted batch is fsynced before the replication offset advances. With weaker fsync policies, crash behavior follows the configured AOF durability contract.
