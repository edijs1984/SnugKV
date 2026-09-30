# First-Release Cluster Bootstrap

This helper implements the bounded cluster-onboarding decision from Phase C2.

It does **not** add a new cluster protocol. It generates and applies the existing
SnugKV cluster, replication, failover, ACL, and health-check configuration for
the two documented first-release three-node layouts.

## Supported layouts

### Sharded

Three primary nodes with all 16,384 slots split into deterministic contiguous
ranges.

### HA

One primary plus two replicas for a single shard. All slots initially belong to
the primary; replica attachment uses the existing `REPLICAOF` path, and
automatic failover uses the existing majority/quorum implementation.

The helper intentionally requires exactly three nodes. General service discovery,
arbitrary topology synthesis, remote process management, and new consensus logic
are out of scope.

## Generate

Sharded example:

```sh
python3 scripts/release/snug-cluster-bootstrap.py generate \
  --mode sharded \
  --nodes 127.0.0.1:7000,127.0.0.1:7001,127.0.0.1:7002 \
  --output /tmp/snug-cluster \
  --password change-me \
  --control-auth change-control-me
```

HA example:

```sh
python3 scripts/release/snug-cluster-bootstrap.py generate \
  --mode ha \
  --nodes 127.0.0.1:7100,127.0.0.1:7101,127.0.0.1:7102 \
  --primary-index 0 \
  --output /tmp/snug-ha \
  --password change-me \
  --control-auth change-control-me \
  --group-id snug-first-release
```

Generation writes:

- `manifest.json`
- `node-0.json`
- `node-1.json`
- `node-2.json`
- `users.acl`

For HA it also derives:

- peer lists;
- majority quorum;
- failover group/epoch settings;
- per-node advertise addresses;
- initial primary slot ownership;
- durable AOF paths.

## Start the nodes

The helper deliberately does not SSH or manage remote services. Start the three
nodes with the generated configs using your process manager, container runtime,
or directly:

```sh
./snugkv -config /tmp/snug-ha/node-0.json
./snugkv -config /tmp/snug-ha/node-1.json
./snugkv -config /tmp/snug-ha/node-2.json
```

## Apply and verify

After all three nodes are running:

```sh
python3 scripts/release/snug-cluster-bootstrap.py apply \
  --manifest /tmp/snug-ha/manifest.json \
  --password change-me
```

For HA, `apply`:

1. waits for all nodes to answer `PING`;
2. attaches the two replicas with `REPLICAOF`;
3. waits for both replication links to stabilize;
4. waits for the primary to become lease-backed and writable;
5. verifies full slot coverage and no active slot transition;
6. verifies failover health/quorum;
7. performs a redirected SET/GET/DEL routing probe through all three nodes.

For sharded mode there is no replication attachment; the same routing and
coverage checks run.

An already configured cluster can be checked independently:

```sh
python3 scripts/release/snug-cluster-bootstrap.py verify \
  --manifest /tmp/snug-ha/manifest.json \
  --password change-me
```

## Validation

Pure generation tests:

```sh
python3 scripts/release/test_snug_cluster_bootstrap.py
```

End-to-end local three-process validation:

```sh
bash scripts/release/run-cluster-bootstrap-smoke.sh
```

The smoke runs both supported layouts on loopback ports, applies the generated
configuration, verifies the cluster, and shuts the processes down.
