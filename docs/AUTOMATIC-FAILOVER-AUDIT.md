# Automatic Failover / Election Audit

Date: 2026-09-27

## Scope

This audit covers SnugKV's first automatic failover implementation for a statically configured replica group.

The implementation builds on the existing replication control plane and deliberately separates three concerns:

1. upstream failure detection;
2. quorum-backed leader election;
3. fenced promotion with renewable majority leases.

It does not yet implement full Sentinel-style topology convergence, service discovery, or automatic reparenting of surviving replicas to the newly promoted leader.

## Configuration

Automatic self-promotion is disabled by default.

`auto_failover_timeout_ms` controls how long a replica must observe its upstream continuously unavailable before failover is considered. A value of zero disables automatic failover.

Peer-backed failover additionally uses:

- `failover_peers`: static peer RESP endpoints;
- `failover_quorum`: required majority;
- `failover_priority`: positive candidate priority, where zero makes a replica ineligible;
- existing `masteruser` / `masterauth` credentials for authenticated failover peer RPC.

Peer mode requires `masterauth`. This prevents unauthenticated clients from participating in election, vote, or lease RPCs.

## Failure detection

A replica records the beginning of a continuous upstream outage.

The outage window:

- starts when following a configured upstream while disconnected;
- resets when the upstream reconnects;
- clears on promotion;
- must exceed `auto_failover_timeout_ms` before the node reports the upstream as down.

## Peer state exchange

Peers exchange a compact JSON state payload through the internal RESP command:

`SNUG.FAILOVER STATE`

The exchanged metadata includes:

- node replication ID;
- current role;
- upstream-down observation;
- processed replication offset;
- failover priority;
- upstream replication lineage (`masterRunID`);
- current election term.

Only peers reporting the same upstream replication lineage are eligible to participate in one election.

## Candidate selection

After enough matching peers independently report the upstream down, candidates are ranked deterministically by:

1. highest processed replication offset;
2. lowest positive failover priority;
3. stable node ID lexical ordering.

A priority of zero means the replica is never a promotion candidate.

Duplicate node observations cannot contribute more than one quorum vote.

Configured quorum must be at least a majority of the static failover group.

## Election terms and durable voting

Each election uses a monotonically increasing term.

A candidate starts at:

`max(local term, observed peer terms) + 1`

Each node may vote for only one candidate in a term.

Vote state consists of:

- election term;
- voted-for candidate ID.

When replication persistence is configured, this state is stored in a dedicated atomic sidecar. A positive vote is not returned until the vote state has been durably written.

Consequences:

- a restart cannot cause a second vote in the same term;
- a higher term supersedes the prior vote;
- repeated requests for the same candidate in the same term are idempotent;
- candidates behind the voter's processed replication offset are rejected.

## Majority lease and fencing

Winning the election is not sufficient for promotion.

The elected candidate must also acquire a majority leader lease for the same term.

Lease properties:

- a peer grants at most one leader lease per term while an existing conflicting lease is live;
- the same leader may renew its lease;
- stale terms are rejected;
- the replication lineage must match;
- lease TTL is bounded;
- the elected leader uses conservative local monotonic deadlines instead of trusting peer wall-clock expiry timestamps.

After promotion, maintenance renews the majority lease before expiry.

If lease quorum is lost and the current majority lease expires, the promoted leader fences writes with:

`READONLY failover leader lease is not valid`

Reads remain available while fenced.

The write fence exists both in generic command execution and the TCP fast-write path so optimized SET handling cannot bypass fencing.

## Promotion

Peer-backed automatic promotion therefore requires all of the following:

1. continuous upstream outage beyond the configured timeout;
2. majority agreement that the upstream is down;
3. deterministic selection of the local node as the best candidate;
4. durable election victory in a new term;
5. majority leader lease acquisition for that same term.

Only then does SnugKV execute the same core promotion transition used by `REPLICAOF NO ONE`, clear upstream continuation state, and become writable.

The promoted node keeps enough failover lineage metadata to renew the lease after its ordinary replica upstream fields are cleared by promotion.

## Authentication boundary

Failover peer RPCs reuse the configured replication ACL credentials.

Peer mode is rejected at configuration validation time unless `masterauth` is configured.

The failover RPC surface is not intended as a public unauthenticated coordination API.

## Validation

Operator-reported race-enabled validation passed for:

- automatic failover timeout behavior;
- reconnect resetting the outage window;
- election quorum and deterministic candidate ranking;
- duplicate-vote protection;
- peer state exchange and lineage filtering;
- durable one-vote-per-term behavior and restart recovery;
- quorum election rounds;
- exclusive and renewable leader leases;
- election + lease gated promotion;
- lease renewal;
- write fencing after quorum loss;
- broad replication, PSYNC, Redis full-sync/partial-resync, REPLICAOF, WAIT/WAITAOF, restart, and config regression coverage.

The branch must still pass the project-wide release gates before merge.

## Remaining Sentinel-like work

This phase implements the safe automatic promotion core, not complete Sentinel compatibility.

Remaining orchestration work includes:

- notifying surviving replicas of the elected leader;
- automatically issuing/reconciling replica reparenting toward the new leader;
- topology convergence after the failed primary returns;
- peer discovery/dynamic membership rather than static configuration;
- richer failover observability and operator controls;
- explicit behavior for membership changes during an active election.

Until reparenting is implemented, operators must treat automatic promotion and automatic topology convergence as separate capabilities.
