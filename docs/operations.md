# Operations

Configuration precedence is defaults, strict JSON file, `SNUGKV_*` environment
variables, then command-line flags. Unknown JSON fields, trailing data, files over
1 MiB, invalid shard counts, unsafe admin/metrics binds, and conflicting AOF and
snapshot paths fail before serving. Most file-level settings require restart;
supported CONFIG parameters can be changed at runtime as documented in
[CONFIG compatibility](CONFIG-COMPATIBILITY.md).

Authentication/ACL persistence is configured with `acl_file` in JSON or
`SNUGKV_ACL_FILE` in the environment. When set, SnugKV loads that ACL file before
accepting clients. A missing or malformed configured ACL file is fatal. `ACL SAVE`
writes password hashes (never plaintext) using a mode-0600 temporary file and
atomic rename; `ACL LOAD` parses into a temporary ACL and replaces the active ACL
only after the entire file validates. See [ACL compatibility](ACL-COMPATIBILITY.md).

SIGINT and SIGTERM stop listeners, close clients, wait for handlers and optimizer
workers, sync/close the AOF, then write the configured snapshot. Startup loads the
snapshot before replaying the AOF. Snapshot corruption or truncation is fatal. A
truncated final AOF frame is discarded; checksum corruption is fatal. A lock file
prevents concurrent AOF writers.

Fsync modes are `always`, `everysec`, and `no`. An append failure rolls back the
client mutation and rejects later writes until restart. `SNUG.AOFREWRITE` writes a
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


## Authentication and ACL operations

With no `acl_file` configured, SnugKV starts with the default user enabled,
`nopass`, `+@all`, and `~*`. Configure an ACL file for durable users:

```json
{
  "acl_file": "/etc/snugkv/users.acl"
}
```

Typical workflow:

```sh
redis-cli -p 6380 ACL SETUSER app on '>secret' resetkeys '~app:*' -@all +@read +set
redis-cli -p 6380 ACL SAVE
redis-cli -p 6380 ACL GETUSER app
```

`ACL LOAD` is atomic with respect to malformed input: a parse failure leaves the
currently active ACL unchanged. Startup uses the same parser and fails closed if
the configured file is invalid.

The audited ACL surface enforces command/category rules, key patterns, classic and
sharded Pub/Sub channel patterns, and root-or-selector rule-set authorization.
Deeper dynamic SORT/script/Function ACL edge auditing remains optional hardening;
see [ACL compatibility](ACL-COMPATIBILITY.md).