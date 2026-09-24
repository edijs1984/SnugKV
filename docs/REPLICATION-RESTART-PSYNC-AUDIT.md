# Replication Restart PSYNC Audit

Date: 2026-09-24

## Scope

SnugKV now persists enough upstream replication state to resume Redis partial synchronization after a graceful SnugKV process restart.

Persisted continuation state:

- upstream host
- upstream port
- upstream replication ID
- last applied upstream offset
- Redis-stream mode

The checkpoint is stored in an atomic sidecar next to the configured AOF or snapshot persistence path.

## Safety model

The restart checkpoint is deliberately written only during graceful shutdown, after the replica dataset has been made durable.

For AOF-backed replicas, SnugKV rewrites the AOF from the exact current logical keyspace before writing the PSYNC checkpoint.

For snapshot-backed replicas, the current logical keyspace snapshot is written before the PSYNC checkpoint.

This avoids a checkpoint whose offset advances beyond the dataset that can actually be recovered after restart.

This milestone does not claim crash-resume PSYNC continuity.

## Live Redis validation

Topology:

- Redis primary: 127.0.0.1:6398
- SnugKV replica: 127.0.0.1:6393
- SnugKV AOF: /tmp/snugkv-restart.aof

Before shutdown, Redis writes replicated successfully:

- restart:a = 1
- restart:b = 2

SnugKV was terminated with SIGTERM and logged:

```
event=stopped
```

The durable files were then present:

```
/tmp/snugkv-restart.aof
/tmp/snugkv-restart.aof.replication
```

The persisted replication state contained the Redis upstream replid and offset:

```
{"version":1,"master_host":"127.0.0.1","master_port":6398,"master_run_id":"1454f522777a28eb83ebd3291606ec0c32797fd9","offset":3383,"redis_stream":true}
```

After restarting SnugKV with the same AOF, Redis logged:

```
Partial resynchronization request from 127.0.0.1:<unknown-replica-port> accepted. Sending 0 bytes of backlog starting from offset 3384.
```

This confirms SnugKV requested Redis's next-byte PSYNC offset convention correctly across process restart.

Recovered values remained present:

```
restart:a = 1
restart:b = 2
```

A new Redis write after restart:

```
SET restart:after-restart yes
```

replicated successfully to SnugKV while `master_link_status` remained `up`.

## Topology invalidation

`REPLICAOF NO ONE` removes the saved continuation checkpoint. Reconfiguring to another upstream also invalidates the prior checkpoint before starting the new follower.

## Compatibility boundary

This implementation guarantees graceful-restart PSYNC continuity when SnugKV persistence is configured and shutdown completes normally.

Unexpected process termination / host crash may still require FULLRESYNC because the upstream offset is not yet committed atomically with each persisted replicated mutation.

