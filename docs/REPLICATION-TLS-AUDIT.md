# Replication TLS Audit

Date: 2026-09-24

## Scope

This audit validates Redis primary -> SnugKV replica synchronization over TLS, including mutual TLS.

Supported upstream replication settings:

- `mastertls`
- `mastertls_ca_cert`
- `mastertls_cert`
- `mastertls_key`
- `mastertls_server_name`

Environment equivalents:

- `SNUGKV_MASTERTLS`
- `SNUGKV_MASTERTLS_CA_CERT`
- `SNUGKV_MASTERTLS_CERT`
- `SNUGKV_MASTERTLS_KEY`
- `SNUGKV_MASTERTLS_SERVER_NAME`

CLI equivalents:

- `-mastertls`
- `-mastertls-ca-cert`
- `-mastertls-cert`
- `-mastertls-key`
- `-mastertls-server-name`

## Security behavior

When upstream TLS is enabled:

- a CA certificate is mandatory
- certificate verification is enabled
- hostname / server-name verification is enabled
- TLS 1.2 is the minimum protocol version
- client certificate and private key must be configured together
- client certificate authentication is optional and supports Redis mTLS
- the existing plain TCP replication path is unchanged when TLS is disabled

No insecure skip-verification mode is exposed.

## Live Redis validation

A native Redis 8.10.2 build compiled with TLS support was used for live interoperability testing.

### Verified TLS + password authentication

Redis was started with:

- TLS-only listener
- server certificate signed by the test CA
- `tls-auth-clients no`
- `requirepass snug-secret`

SnugKV was started with:

```
-mastertls
-mastertls-ca-cert /tmp/snugkv-tls/ca.crt
-mastertls-server-name localhost
-masterauth snug-secret
```

Observed:

- Redis TLS endpoint returned PONG with CA verification
- SnugKV TLS handshake succeeded
- upstream AUTH succeeded
- full sync completed
- `master_link_status:up`
- `master_sync_in_progress:0`
- live writes propagated over TLS
- replica remained READONLY

### Mutual TLS

Redis was restarted with:

```
--tls-auth-clients yes
```

SnugKV was started with:

```
-mastertls
-mastertls-ca-cert /tmp/snugkv-tls/ca.crt
-mastertls-cert /tmp/snugkv-tls/client.crt
-mastertls-key /tmp/snugkv-tls/client.key
-mastertls-server-name localhost
-masterauth snug-secret
```

Observed:

- SnugKV presented the client certificate successfully
- Redis accepted the mTLS connection
- authenticated replication completed
- `master_link_status:up`
- `master_sync_in_progress:0`
- live writes propagated
- replica remained READONLY

## Automated tests

Coverage includes:

- TLS configuration validation
- TLS environment configuration
- successful verified TLS dialing with a trusted CA
- invalid CA rejection
- existing replication authentication tests
- existing replication phase tests
- EOF-framed full-sync tests

## Validation gates

The following passed:

```
go test ./internal/config ./internal/engine ./internal/server -count=1
go test -race ./internal/config ./internal/engine ./internal/server -count=1
go test -race -count=1 ./...
go vet ./...
git diff --check
```

## Remaining replication hardening

- broader Redis RDB object encodings as demanded by real datasets
- broader failover / Sentinel-like behavior
- exact Redis wire-offset semantics beyond the currently audited paths
