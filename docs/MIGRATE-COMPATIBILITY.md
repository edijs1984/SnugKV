# Redis MIGRATE Compatibility

SnugKV implements Redis-compatible `MIGRATE` for the audited single-node Redis 8.2 surface.

## Supported syntax

```text
MIGRATE host port key destination-db timeout
        [COPY] [REPLACE]
        [AUTH password]
        [AUTH2 username password]
        [KEYS key [key ...]]
```

SnugKV exposes only logical database 0, but it interoperates with Redis targets through the standard `SELECT <db>` + `RESTORE` pipeline. As a MIGRATE destination, SnugKV accepts `SELECT 0`; nonzero database selection remains the documented single-database boundary.

## Transfer behavior

The implementation uses Redis-compatible DUMP payloads and transfers each key with `RESTORE`.

Audited behavior includes:

- ordinary move semantics: successful target restore deletes the local source key;
- `COPY`: successful target restore leaves the source key intact;
- `REPLACE`: target keys can be overwritten;
- TTL transfer in milliseconds;
- missing source keys return `NOKEY`;
- `KEYS` multi-key mode;
- `AUTH` and `AUTH2`;
- Redis-style target error wrapping;
- Redis-style connect/read/write IOERR classes;
- timeout values <= 0 normalized to 1000 ms.

## Partial multi-key failure semantics

Redis MIGRATE is not all-or-nothing across a KEYS batch.

RESTORE commands are pipelined to the target and replies are processed in order. For every successful target reply, a non-COPY source key is deleted locally immediately. If a later key fails, for example with BUSYKEY, earlier successful moves remain committed while later failed keys remain at the source.

SnugKV matches that observable behavior. Partial local deletions are also persisted through the logical journal and invalidate WATCH state.

## Live Redis 8.2 interoperability

The following directions were tested live:

- SnugKV -> Redis 8.2;
- Redis 8.2 -> SnugKV.

Audited cases include:

- basic move;
- COPY;
- destination collision;
- REPLACE;
- TTL transfer;
- missing key / NOKEY;
- KEYS multi-key move;
- KEYS partial collision;
- AUTH;
- AUTH2;
- invalid db / timeout syntax.

The Redis -> SnugKV test uses the real Redis MIGRATE implementation against SnugKV's SELECT + RESTORE destination path.

## Scope boundary

SnugKV remains a single-database server. It can migrate to Redis logical databases selected by the destination-db argument, but when SnugKV itself is the destination only DB 0 is available.

Cluster ASKING / RESTORE-ASKING semantics are outside the current standalone scope.

## Audit harnesses

- `compat/keyspace/migrate-wire.py`
- `compat/keyspace/migrate-cross.py`
- `compat/keyspace/migrate-auth-cross.py`
