# Client Command Trace

## Result

Supported real-client workflows exercised **0 of the 58 A2 candidate-gap commands**.

Clients covered:

- ioredis 6.0.0
- node-redis 6.2.1
- redis-py 6.4.0
- go-redis v9.22.0

Workflows covered:

- RESP2 connection and basic GET/SET
- RESP3 connection and representative data types
- reconnect
- pipelines
- MULTI / EXEC
- Pub/Sub
- client-side tracking / caching flow
- Cluster discovery and routing
- cross-shard pipelines
- persistent Cluster clients across automatic primary failover

## Candidate command result

- candidate total: **58**
- candidate observed: **0**
- candidate not observed: **58**

This means none of the Redis-only A2 candidate commands are REQUIRED solely for
the supported first-release client/workflow matrix.

## Important compatibility finding

A3 did uncover a real Cluster reply-shape defect outside the 58 candidate
commands.

Before the fix, `CLUSTER SLOTS` returned only the slot owner. In a one-shard
HA topology a client could therefore learn only the current master and have no
surviving topology endpoint after that process died.

A3 changed `CLUSTER SLOTS` to advertise the shard replicas after the master
entry. Focused tests require the replica endpoints to be present.

Persistent failover validation then passed with the same client objects:

- ioredis survived promotion of 127.0.0.1:7121 after 127.0.0.1:7120 was killed;
- node-redis survived the same failover when configured with all HA members as
  discovery roots, as its cluster API expects;
- both clients wrote and read successfully after topology convergence;
- the old primary restarted, demoted to replica, reparented, and rejected stale
  writes;
- the complete multi-process failover crash/restart chaos run passed.

## Interpretation

A3 evidence does **not** justify implementing Redis commands merely because they
exist in Redis 8.2.

Phase A4 should:

1. mark unsupported candidate commands REQUIRED only if another explicit
   first-release workflow needs them;
2. promote a small number to USEFUL only where they materially improve
   operability or drop-in usability;
3. DEFER the remainder;
4. separately schedule structured COMMAND metadata completion for already
   implemented subcommands.
