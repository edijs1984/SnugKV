# Replication ACK / Observability Differential Audit

## Scope

This audit covers the Replication Phase 2 ACK/observability slice:

- `REPLCONF ACK <offset>` handling on replica links;
- per-replica acknowledged replication offsets;
- monotonic ACK semantics;
- per-replica lag tracking;
- `INFO replication` replica lines with online state, offset, and lag;
- periodic ACK emission from SnugKV replicas.

This audit does not cover Redis RDB full-sync interoperability, topology authentication/TLS, or Sentinel/failover behavior.

## Redis oracle

Harness:

`compat/replication/replication-ack-oracle.py`

The Redis 8.2 oracle measured:

- replica visibility after full sync;
- `state=online`;
- integer replica offset;
- integer lag;
- explicit ACK advancement to the current master replication offset;
- fresh ACK lag;
- stale/lower ACKs not moving the acknowledged offset backwards.

## SnugKV implementation

The primary stores ACK offset and last-ACK time per connected replica.

`REPLCONF ACK <offset>` updates the acknowledged offset only when the new offset is greater than the currently recorded value. ACK receipt refreshes the replica's ACK timestamp.

`INFO replication` reports Redis-shaped replica lines:

`slaveN:ip=127.0.0.1,port=0,state=online,offset=<ack>,lag=<seconds>`

SnugKV replicas also emit periodic `REPLCONF ACK` messages while following an upstream primary.

## Validation

Focused unit and race-enabled replication tests passed, including ACK monotonicity and INFO replication coverage.

The same oracle was run against Redis 8.2 and SnugKV.

Final comparison:

```bash
diff -u \
  /tmp/replication-ack-redis.txt \
  /tmp/replication-ack-snug.txt
```

returned an empty diff.

## Result

The audited ACK/observability surface matches the measured Redis 8.2 behavior exactly.

## Remaining Replication Phase 2 hardening

Still open:

- Redis RDB full-sync interoperability;
- additional diskless full-sync transport hardening;
- topology authentication;
- TLS topology support;
- failover/Sentinel-like orchestration.
