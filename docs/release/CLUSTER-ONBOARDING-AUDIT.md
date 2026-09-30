# First-Release Cluster Onboarding Audit

## Scope

This audit measures the current clean-start workflow for a small SnugKV cluster.
It does not evaluate steady-state correctness; replication, automatic failover,
slot routing, membership recovery, resharding, and restart convergence already
have separate hardening evidence.

The first-release question is narrower:

> Can a new operator create the supported small cluster without manually
> reproducing topology state across several configuration files and commands?

## 1. Routing-only three-node cluster

The existing `scripts/cluster-client-smoke.sh` demonstrates the minimum static
routing topology.

Each process requires these cluster-specific settings:

1. `cluster_enabled: true`
2. its own `cluster_node_addr`
3. the shared `cluster_control_auth`
4. the complete `cluster_slots` ownership map

For a three-primary topology the same 16,384-slot ownership map is repeated in
all three node configurations.

Example ownership:

- 0-5460 -> node A
- 5461-10922 -> node B
- 10923-16383 -> node C

### Operator work

Before startup the operator must know and reproduce:

- all three advertised addresses;
- the complete slot partition;
- one shared internal-control secret;
- one per-node local address.

Post-start operator commands: **0** for the static routing-only topology.

### Node IDs

Node IDs are derived from node addresses. The operator does **not** manually copy
or assign node IDs.

### Restart

With the same configuration/persisted topology, ordinary restart does not require
manual topology reconstruction.

## 2. Three-node HA shard with automatic failover

The existing `scripts/cluster-chaos-failover-restart.sh` demonstrates the
supported HA topology: one initial primary plus two replicas.

In addition to the cluster settings above, each node currently needs failover
configuration including:

- `masterauth` for authenticated peer/replication RPC;
- `auto_failover_timeout_ms`;
- `failover_peers` (the other members);
- `failover_quorum`;
- `failover_group_id`;
- `failover_config_epoch`;
- `failover_advertise_addr`;
- optionally differentiated `failover_priority`.

The practical hardened example also configures ACL and durable persistence.

All nodes repeat the initial cluster slot map, which initially assigns the shard
to the primary.

### Required post-start commands

After all three processes start, the two replicas are explicitly attached:

```text
REPLICAOF <primary-host> <primary-port>
REPLICAOF <primary-host> <primary-port>
```

For N nodes in a single HA shard this is N-1 explicit replication-attachment
commands.

Automatic failover then handles election, promotion, reparenting, old-primary
demotion, and slot-owner convergence without an operator command.

## 3. Existing membership/discovery mechanisms

### CLUSTER JOIN

`CLUSTER JOIN <addr>` safely converges the known-node registry, but it is not an
initial topology bootstrap primitive.

The candidate must already:

- have cluster support enabled;
- advertise the expected address;
- have the same slot-ownership digest as the existing cluster.

Therefore JOIN does not eliminate the need to preconfigure a matching
`cluster_slots` map.

### Failover discovery seeds

`failover_discovery_seeds` can discover peers in an already identified failover
group, but discovery requires:

- a group ID;
- a nonzero config epoch;
- authenticated peer RPC.

Discovery does not automatically change voting membership. Adoption is a
separate guarded operation through the discovery-plan/adoption path.

This is appropriate for safe membership evolution, but it is not a zero-state
bootstrap mechanism.

## 4. Measured onboarding burden

### Routing-only topology

- addresses the operator must coordinate: **3**
- explicit slot ranges the operator must assign: **3**
- cluster-specific config concepts: **4**
- repeated full topology maps: **3 copies**
- post-start commands: **0**

### Three-node HA shard

Beyond ordinary listen/ACL/persistence settings:

- cluster-specific config concepts: **4**
- failover-specific config concepts: **7 required + 1 optional priority**
- peer lists: **2 peer addresses per node**
- repeated initial full slot map: **3 copies**
- post-start replication commands: **2**
- manual node-ID copying: **0**
- manual failover action after a primary crash: **0**
- manual stale-primary repair after restart: **0**

## 5. Failure-prone manual inputs

The current workflow has several places where equivalent information must agree
across nodes:

- advertised addresses;
- slot ownership;
- failover group ID;
- membership epoch;
- quorum;
- peer lists;
- internal-control credential;
- replication credential.

The implementation validates many malformed combinations and rejects mismatched
topology safely. That is good correctness behavior, but the amount of duplicated
input is still onboarding friction.

## 6. Audit conclusion

The current explicit workflow is acceptable for tests and expert operators, but
it is **not sufficiently simple for the first-release quickstart target**.

The missing piece is not another general cluster subsystem. The existing runtime
already has membership, discovery, replication, failover, recovery, and guarded
topology mutation.

The first-release onboarding gap is a thin bootstrap layer that converts one
small declarative topology into the already-supported per-node configuration and
attachment operations.

## 7. Recommended C2 decision

Choose **Option 2 — bounded automatic admission/bootstrap is required**.

Keep the scope narrow:

1. support the documented small first-release topology;
2. accept a concise list of node addresses and desired primary/replica layout;
3. derive deterministic slot ranges;
4. derive per-node peer lists/quorum/group configuration;
5. generate or apply the existing node configurations;
6. attach replicas using the existing replication path;
7. verify `CLUSTER HEALTH`, `CLUSTER CONSISTENCY`, replication state, and
   failover health;
8. do not build a service-discovery platform, scheduler, or new consensus layer.

A script/CLI wrapper over the current hardened primitives is sufficient if it can
be run from a clean deployment without manually duplicating topology state.
