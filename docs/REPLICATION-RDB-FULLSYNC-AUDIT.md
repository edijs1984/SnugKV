# Replication Redis RDB Full-Sync Interoperability Audit

Date: 2026-09-23

## Scope

This audit validates Redis 8.2 primary -> SnugKV replica full synchronization using Redis's real RDB snapshot format, followed by the live Redis replication command stream.

The implementation intentionally keeps SnugKV -> SnugKV replication on SnugKV's logical checksummed frames. Redis RDB parsing is used only when the upstream FULLRESYNC snapshot is identified by the REDISxxxx magic.

## Redis oracle

Redis 8.2.9 produced a length-prefixed FULLRESYNC snapshot with REDIS magic, RDB version 0012, valid Redis CRC64, AUX metadata, database 0 selection, 7 keys, and 1 expiring key. redis-check-rdb accepted the captured snapshot with checksum OK.

## Supported imported object encodings

The audited importer supports STRING, HASH listpack, SET intset/listpack, LIST quicklist2, ZSET listpack, and STREAM listpacks3.

Handled metadata: AUX, SELECTDB (DB 0 only), RESIZEDB, EXPIRETIME, EXPIRETIME_MS, IDLE, FREQ, EOF, and Redis CRC64.

Unsupported object types/opcodes fail closed. Nonzero logical databases are rejected because SnugKV is intentionally single-database.

## Transport behavior

For Redis length-prefixed replication snapshots, SnugKV consumes exactly the advertised RDB byte count. It does not expect the SnugKV logical-frame trailing CRLF because Redis's replication command stream can begin immediately after the RDB payload.

EOF-marker/diskless snapshot framing remains hardening work.

## Live validation

A live Redis 8.2.9 primary on port 6395 was followed by a fresh SnugKV process on port 6393.

Full-sync validation succeeded for STRING, HASH, SET, LIST, ZSET, STREAM, and absolute TTL restoration. A fresh one-hour TTL was restored with a positive remaining TTL close to the Redis source value.

Post-snapshot live replication succeeded for SET, INCR, HSET, RPUSH, and SET with PX expiry. SnugKV continued to reject direct client writes with Redis-style READONLY behavior.

## Validation gates

The following passed on the feature branch:

go test ./internal/engine ./internal/server -count=1
go test -race ./internal/engine ./internal/server -count=1
go test -race -count=1 ./...
go vet ./...
git diff --check

Focused replication/RDB tests also passed under the race detector.

## Boundaries / remaining work

This audit does not claim complete Redis RDB compatibility. Remaining replication hardening includes EOF-marker/diskless full-sync framing, additional RDB encodings as required by real datasets, topology authentication, TLS replication topology, broader failover/Sentinel work, and exact Redis wire-offset semantics beyond the audited path.
