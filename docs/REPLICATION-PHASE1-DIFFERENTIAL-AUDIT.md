# Replication Phase 1 Differential Audit

## Scope

This audit covers SnugKV Replication Phase 1 against Redis 8.2 for the implemented primary/replica surface.

Implemented Phase 1 behavior:

- `REPLICAOF host port`
- `REPLICAOF NO ONE`
- `ROLE`
- `INFO replication`
- Redis-shaped `PSYNC ? -1` / `FULLRESYNC` control flow
- initial full logical dataset synchronization
- live committed-write propagation
- absolute TTL preservation
- replica read-only enforcement
- promotion back to writable standalone mode
- transaction propagation
- replica disconnect cleanup and ordered live streaming

## Architecture boundary

SnugKV uses Redis-shaped replication control semantics, but Phase 1 does not use Redis RDB bytes as its synchronization payload.

The replication transport reuses SnugKV's checksummed logical persistence-record frames. During full synchronization the primary holds the durability serialization boundary, exports the logical snapshot, registers the replica stream, sends the `FULLRESYNC` response plus snapshot, and then releases the boundary so subsequent committed writes enter the live stream in order.

This is an intentional implementation boundary, not a claim of Redis RDB replication interoperability.

## Differential oracle

Reusable harness:

`compat/replication/replication-oracle.py`

The same oracle was run against:

1. a Redis primary/replica pair to capture the Redis 8.2 baseline;
2. a two-process SnugKV primary/replica pair.

SnugKV live topology:

- primary: `127.0.0.1:6393`
- replica: `127.0.0.1:6394`

The oracle normalizes random replication IDs and offsets before comparison.

Comparison command:

```bash
diff -u \
  /tmp/replication-redis.txt \
  /tmp/replication-snug.txt
```

Result:

```text
<empty diff>
```

Therefore the audited live SnugKV output matched the Redis baseline exactly after normalization.

## Audited behavior

The differential covers the Phase 1 observable surface, including:

- standalone primary `ROLE` state;
- replication configuration and role transition;
- Redis-shaped full-resynchronization handshake;
- initial dataset synchronization;
- TTL synchronization;
- live `SET` propagation;
- live `INCR` propagation;
- live `HSET` propagation;
- replica write rejection using Redis `READONLY` error semantics;
- promotion with `REPLICAOF NO ONE`;
- post-promotion writable behavior.

Focused regression coverage additionally validates transaction replication and server integration paths.

## Validation gates

The Phase 1 branch was validated with:

```bash
go test ./internal/engine ./internal/server -count=1

go test -race ./internal/engine ./internal/server -count=1

go test -race -count=1 ./...

go vet ./...
```

All gates passed locally.

## Deferred to Phase 2

The following are intentionally outside Replication Phase 1:

- partial resynchronization;
- replication backlog;
- replica ACK offset accounting;
- reconnect continuation without full synchronization;
- Redis RDB full-sync interoperability;
- topology authentication;
- TLS topology support;
- automatic failover / Sentinel-like orchestration.

## Result

For the audited Phase 1 surface, SnugKV matches Redis 8.2 externally observable replication behavior after normalization of implementation-specific replication IDs and offsets.

Replication Phase 1 is complete.
