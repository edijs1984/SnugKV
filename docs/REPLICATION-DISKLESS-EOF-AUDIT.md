# Replication Diskless EOF Full-Sync Audit

Date: 2026-09-24

## Scope

This audit validates Redis 8.2 diskless full synchronization using the `$EOF:<40-byte marker>` framing path, followed by normal live replication into SnugKV.

## Redis oracle

A live Redis 8.2 primary accepted:

- `REPLCONF capa eof`
- `PSYNC ? -1`

and returned:

- `+FULLRESYNC`
- `$EOF:<40-byte marker>`
- an RDB payload with `REDIS` magic
- RDB version 12
- valid Redis CRC64

The captured EOF-framed snapshot contained a test string and expiring key.

## SnugKV behavior

SnugKV now advertises the EOF replication capability before PSYNC. The follower:

- accepts Redis EOF-framed snapshots
- validates the 40-byte EOF marker
- reads until the exact marker without consuming the following replication stream
- works independently of the internal bufio.Reader capacity
- rejects malformed marker lengths
- continues through the existing Redis RDB decoder
- transitions into the live Redis replication command stream
- keeps replica READONLY semantics

The follower tolerates a pre-PSYNC REPLCONF error so older SnugKV primaries remain compatible.

## Live end-to-end validation

Redis primary: port 6395
SnugKV replica: port 6393

Observed after full sync:

- `master_link_status:up`
- `master_sync_in_progress:0`
- initial EOF-snapshot string restored
- initial EOF-snapshot TTL restored and positive
- source and replica TTLs remained close
- live SET/HSET/SET PX propagation succeeded
- direct client writes to the replica returned READONLY

## Validation gates

The following passed:

```
go test ./internal/engine ./internal/server -count=1
go test -race ./internal/engine ./internal/server -count=1
go test -race -count=1 ./...
go vet ./...
git diff --check
```

Focused EOF/RDB/replication tests also passed under the race detector.

## Remaining replication hardening

- additional Redis RDB object encodings as demanded by real datasets
- topology authentication / ACL credentials
- TLS replication topology
- broader failover / Sentinel-like behavior
- exact Redis wire-offset semantics beyond the currently audited paths
