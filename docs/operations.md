# Operations

Configuration precedence is defaults, strict JSON file, `MORPHCACHE_*` environment
variables, then command-line flags. Unknown JSON fields, trailing data, files over
1 MiB, invalid shard counts, unsafe admin/metrics binds, and conflicting AOF and
snapshot paths fail before serving. Settings require restart.

SIGINT and SIGTERM stop listeners, close clients, wait for handlers and optimizer
workers, sync/close the AOF, then write the configured snapshot. Startup loads the
snapshot before replaying the AOF. Snapshot corruption or truncation is fatal. A
truncated final AOF frame is discarded; checksum corruption is fatal. A lock file
prevents concurrent AOF writers.

Fsync modes are `always`, `everysec`, and `no`. An append failure rolls back the
client mutation and rejects later writes until restart. `MORPH.AOFREWRITE` writes a
checksummed reset frame and complete logical state to a temporary file, syncs it,
and atomically replaces the AOF.

Eviction policies are `noeviction`, `allkeys-lru`, and `volatile-lru`. Under
pressure the server reclaims expired entries, compacts capacity, then samples
bounded LRU candidates. Pending command keys are excluded. Evictions are journaled
before deletion when AOF is enabled.

Prometheus text metrics are available at `/metrics` on the optional loopback-only
HTTP listener. They expose commands, latency, connections, traffic, logical and
encoded bytes, accounting, expiration/eviction, codecs, schemas/dictionaries, and
optimizer activity. Keys and values are omitted.
