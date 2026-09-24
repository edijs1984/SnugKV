# Replication Authentication Audit

Date: 2026-09-24

## Scope

This audit validates authenticated Redis 8.2 primary -> SnugKV replica synchronization for both Redis authentication forms:

- password-only authentication via `AUTH <password>`
- ACL authentication via `AUTH <username> <password>`

SnugKV exposes Redis-compatible upstream replication settings:

- `masterauth`
- `masteruser`
- `SNUGKV_MASTERAUTH`
- `SNUGKV_MASTERUSER`
- `-masterauth`
- `-masteruser`

## Handshake behavior

When `masterauth` is configured, SnugKV authenticates before any replication capability negotiation or PSYNC request.

Behavior:

- `masterauth` only -> `AUTH <password>`
- `masteruser` + `masterauth` -> `AUTH <username> <password>`
- no configured credentials -> authentication step is skipped

Authentication failures return a generic replication-authentication error and do not include the configured username or password.

## Live Redis validation

### Password-only primary

Redis was started with `requirepass snug-secret`.

SnugKV was started with:

```
-masterauth snug-secret
```

Observed:

- `REPLICAOF 127.0.0.1 6396` succeeded
- `master_link_status:up`
- `master_sync_in_progress:0`
- initial string restored
- expiring key restored
- replica PTTL remained positive and close to the source
- replica remained READONLY

### ACL primary

Redis was started with the default user disabled and a dedicated ACL user:

```
user repluser on >repl-secret ~* +@all
```

SnugKV was started with:

```
-masteruser repluser
-masterauth repl-secret
```

Observed:

- ACL AUTH succeeded
- full sync completed
- `master_link_status:up`
- `master_sync_in_progress:0`
- string and TTL state restored correctly
- replica remained READONLY

## Automated tests

Focused tests validate:

- password-only AUTH command shape
- ACL username/password AUTH command shape
- credential redaction on authentication failure
- existing replication phases remain green
- Redis RDB and EOF-framed full-sync paths remain green

## Validation gates

The following passed:

```
go test ./internal/engine ./internal/server -count=1
go test -race ./internal/engine ./internal/server -count=1
go test -race -count=1 ./...
go vet ./...
git diff --check
```

## Remaining replication hardening

- broader Redis RDB object encodings as demanded by real datasets
- TLS replication topology
- broader failover / Sentinel-like behavior
- exact Redis wire-offset semantics beyond the currently audited paths
