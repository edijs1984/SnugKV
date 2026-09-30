# First-Release Client Compatibility Matrix

This matrix records retained first-release evidence for the supported Redis client
libraries. A PASS means the named client/workflow was exercised against SnugKV;
it does not imply exhaustive coverage of every library feature.

Client versions:

- ioredis 6.0.0
- node-redis 6.2.1
- redis-py 6.4.0
- go-redis/v9 9.22.0

## Matrix

| Client | Standalone | RESP3 | Pool/reconnect | Pub/Sub | Tracking/caching | Cluster | Automatic failover |
|---|---|---|---|---|---|---|---|
| ioredis 6.0.0 | PASS | PASS | PASS | not separately release-gated | not separately release-gated | PASS | PASS |
| node-redis 6.2.1 | PASS | PASS | PASS | PASS | PASS | PASS | PASS |
| redis-py 6.4.0 | PASS | PASS | PASS | not separately release-gated | not separately release-gated | PASS | PENDING D failover matrix |
| go-redis/v9 9.22.0 | PASS | PASS | PASS | not separately release-gated | not separately release-gated | PASS | PENDING D failover matrix |

## Evidence

### Standalone / reconnect

Retained standalone smoke covers ordinary command use, binary round trips,
pipelines, and reconnect behavior for all four libraries.

Relevant files:

- `compat/node/smoke.cjs`
- `compat/python/smoke.py`
- `compat/go/main.go`

### RESP3

Representative RESP3 data/reply handling, transactions, pipelines, and reconnect
are retained for all four supported libraries.

Relevant files:

- `compat/node/resp3-smoke.cjs`
- `compat/python/resp3_smoke.py`
- `compat/go/resp3/main.go`

### Pub/Sub and client-side tracking

The A3 release workflow explicitly validates RESP3 Pub/Sub plus
`CLIENT TRACKING` / `CLIENT CACHING` with node-redis in
`compat/node/trace-workflows.cjs`.

Those workflows are not currently a per-library release gate. The matrix therefore
does not infer PASS for ioredis, redis-py, or go-redis without dedicated evidence.

### Cluster routing

Normal Cluster startup/discovery, redirected routing, same-slot multi-key
operations, and cross-shard pipelines are retained for all four libraries.

Relevant files:

- Node client cluster smoke
- `compat/python/cluster_smoke.py`
- `compat/go/cluster/main.go`
- `scripts/cluster-client-smoke.sh`

### Automatic failover

A3 retained persistent automatic-failover recovery for:

- ioredis
- node-redis

Phase D adds persistent clients for:

- redis-py: `compat/python/failover_trace.py`
- go-redis: `compat/go/failover/main.go`

All four are exercised together by:

```sh
bash scripts/release/run-client-failover-matrix.sh
```

The test keeps the same client objects alive while the primary is killed,
waits for automatic election/topology convergence, then requires each client to
write/read successfully through the promoted primary.

## Release interpretation

A client/workflow is first-release supported when its required cells above are
PASS and there is no unexplained compatibility failure.

Pub/Sub/tracking are treated as explicit workflow capabilities rather than a
claim that every client library's convenience API has been separately audited.

Phase D is complete when the redis-py and go-redis failover cells become PASS.
