# Hash Field Expiration / Redis Replication Audit

Date: 2026-09-24

## Scope

This audit covers Redis-compatible hash-field expiration (HFE) support in SnugKV and Redis -> SnugKV replication interoperability for Redis RDB hash metadata carrying field expirations.

Implemented command surface:

- `HEXPIRE`
- `HPEXPIRE`
- `HEXPIREAT`
- `HPEXPIREAT`
- `HTTL`
- `HPTTL`
- `HEXPIRETIME`
- `HPEXPIRETIME`
- `HPERSIST`

Conditional expiry modifiers implemented with Redis-compatible per-field semantics:

- `NX`
- `XX`
- `GT`
- `LT`

## Engine representation

Packed hashes now support a versioned field-expiry representation carrying an absolute millisecond expiration per field.

Per-field expiration metadata survives SnugKV logical export/restore.

Expired fields are hidden by HASH reads and scans, and HASH mutation paths account for expired fields before applying updates.

## Redis command result semantics

The audited HFE commands return Redis-shaped per-field arrays.

Expiry operations:

- `-2`: field/key does not exist
- `0`: condition not met
- `1`: expiration set/updated
- `2`: field deleted because the requested expiry is already in the past

`HPERSIST`:

- `-2`: field/key does not exist
- `-1`: field exists but has no field TTL
- `1`: field TTL removed

TTL/time queries:

- `-2`: field/key does not exist
- `-1`: field exists without a field TTL
- non-negative value: remaining TTL or absolute expiration time

For fields without an existing field TTL:

- `NX` succeeds
- `LT` succeeds
- `XX` fails
- `GT` fails

## Live Redis differential

Redis 8.10.2 and SnugKV were run side by side with the same HASH data and absolute expiration timestamps.

Validated:

- `HPEXPIREAT`
- `HPEXPIRETIME`
- `HPERSIST`
- `HPTTL`
- immediate/past expiration deletion
- `HEXPIREAT`
- `HEXPIRETIME`
- `HTTL`
- `NX`
- `XX`
- `GT`
- `LT`

Absolute expiration outputs matched exactly. Relative TTL output differed only by normal wall-clock execution drift.

## Redis RDB interoperability

SnugKV now decodes Redis HFE hash metadata RDB encodings:

- type 22: `HASH_METADATA_PRE_GA`
- type 24: `HASH_METADATA`

Type 24 reconstruction follows Redis' minimum-expiry plus relative-TTL encoding:

`expireAt = minExpire + ttl - 1`

Synthetic decoder tests cover both formats.

A live Redis 8.10.2 master generated a real HFE hash in `hashtable` encoding and full-synced it into SnugKV.

Observed Redis source values:

- persistent field: no TTL
- short field absolute expiration preserved exactly
- later field absolute expiration preserved exactly

After full sync SnugKV reported:

- `master_link_status:up`
- all HASH fields present
- persistent field `HPEXPIRETIME = -1`
- expiring fields with the exact Redis absolute expiration timestamps
- positive remaining `HPTTL`

This validates the type-24 path against Redis-produced bytes, not only synthetic fixtures.

## Redis partial-resync reconnect regression

During live testing, Redis logs exposed a reconnect loop after a Redis `+CONTINUE` partial resynchronization.

Root cause:

1. A Redis `+FULLRESYNC` correctly selected Redis RESP replication-stream parsing.
2. On a later `+CONTINUE`, SnugKV did not restore that stream mode.
3. Redis RESP commands were then parsed as SnugKV logical replication frames.
4. The parse failed, the socket closed, and SnugKV retried after 250 ms.

Fix:

- replication state now remembers whether the current upstream uses the Redis command stream;
- `+CONTINUE` restores that mode;
- changing upstream or promoting clears it;
- regression coverage verifies a Redis RESP write after `+CONTINUE`.

The focused test and race test pass.

Live Redis logs no longer show the repeating ~250 ms accepted-partial-resync / connection-lost loop.

## Restart behavior

A SnugKV process restart currently loses the in-memory upstream Redis replication ID and offset. After process restart, SnugKV therefore requests a full resync.

This is distinct from the fixed same-process partial-resync reconnect bug.

Persisting upstream PSYNC state across SnugKV process restarts remains future work.

## Known remaining HFE interoperability work

The current Redis RDB HFE decoder covers the hashtable metadata formats 22 and 24.

Still open:

- Redis `HASH_LISTPACK_EX_PRE_GA` type 23
- Redis `HASH_LISTPACK_EX` type 25
- newer Redis hash-template RDB formats where relevant
- active/background removal of fully expired HASH keys for exact keyspace-level parity
- persistence of upstream Redis replid/offset for partial resync after SnugKV process restart

## Verification

Focused tests passed for:

- packed HFE round trip and visibility
- HFE command behavior
- NX/XX/GT/LT semantics
- Redis type-22/type-24 RDB decoding
- Redis partial-resync stream-mode preservation
- race coverage of the reconnect regression

The repository test/race/vet/diff gate was reported green before closing this audit slice.
