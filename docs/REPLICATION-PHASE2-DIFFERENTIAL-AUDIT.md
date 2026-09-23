# Replication Phase 2 Core Differential Audit

## Scope

This audit covers the first Replication Phase 2 slice:

- replication backlog activation and retention;
- Redis-shaped replication offsets;
- reconnect continuation with `PSYNC <replid> <offset>`;
- `+CONTINUE` when the requested next byte is still present in backlog;
- fallback to `FULLRESYNC` when the requested offset cannot be served;
- replica-side persistence of upstream replid/offset across reconnect attempts;
- continued backlog capture while no replica is connected.

This audit does **not** claim completion of the remaining Phase 2 hardening items such as replica ACK accounting, Redis RDB full-sync interoperability, topology authentication/TLS, or automatic failover.

## Redis oracle

Harness:

`compat/replication/replication-phase2-oracle.py`

The oracle measures:

- `INFO replication` backlog metadata shape;
- full sync establishment;
- replication offset advancement after writes;
- partial resynchronization using the Redis next-byte offset convention;
- invalid/future offset fallback to full synchronization.

The Redis baseline was captured against Redis 8.2.

## Important PSYNC offset rule

The audit confirmed that Redis replicas reconnect using the **next byte needed**:

`last_processed_offset + 1`

SnugKV now follows the same convention.

## SnugKV behavior

SnugKV maintains a bounded logical replication backlog once replication is activated. New committed replication frames continue entering the backlog even when no replica is currently connected.

A reconnecting replica presents the upstream replid plus its next required offset. If the requested position is still covered by the backlog, the primary replies:

`+CONTINUE`

and sends only the missing logical replication frames.

If the requested replid/offset cannot be served, SnugKV falls back to:

`+FULLRESYNC`

followed by the full logical snapshot transfer.

## Regression evidence

Focused race-enabled tests passed:

```bash
go test -race ./internal/server \
  -run 'TestReplicationPhase(1|2)|TestReplicationRoleAndInfoShape' \
  -count=1 -v
```

The Phase 2 reconnect regression also uses a replica-local sentinel key. A successful partial resync preserves that sentinel, while a hidden full resync would clear it. This proves the reconnect path actually used partial continuation.

## Live differential

The same updated oracle was run against:

- Redis 8.2 on port 6395;
- SnugKV on port 6393.

Both produced:

- full sync: `FULLRESYNC`;
- backlog active after replication starts;
- replication offset advancement;
- partial sync: `CONTINUE`;
- future offset fallback: `FULLRESYNC`.

The final comparison:

```bash
diff -u \
  /tmp/replication-phase2-redis-v3.txt \
  /tmp/replication-phase2-snug-v3.txt
```

returned an empty diff.

## Remaining Phase 2 work

Still open:

- replica ACK offset accounting;
- backlog/ACK observability hardening;
- Redis RDB full-sync interoperability;
- topology authentication;
- TLS topology support;
- further diskless-transfer interoperability hardening;
- automatic failover / Sentinel-like behavior.

## Result

The audited Replication Phase 2 **partial-resynchronization core** matches the measured Redis 8.2 behavior exactly.
