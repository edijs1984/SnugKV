# Redis HFE LISTPACK_EX RDB Audit

Date: 2026-09-24

## Scope

This audit covers Redis -> SnugKV RDB full-sync compatibility for hash field expiration (HFE) hashes stored using Redis' LISTPACK_EX encoding.

Supported Redis RDB object types:

- 23: `HASH_LISTPACK_EX_PRE_GA`
- 25: `HASH_LISTPACK_EX`

## Redis layout

Redis LISTPACK_EX hashes store triplets in the listpack:

`field, value, absolute-expiry-ms`

A TTL value of `0` means the field has no field expiration.

Current type 25 additionally stores an 8-byte little-endian minimum-expiration timestamp before the encoded listpack. The per-field expiration values inside the listpack remain absolute timestamps.

## SnugKV decoder behavior

SnugKV now:

- reads the type-25 min-expiry prefix;
- decodes the underlying Redis listpack;
- requires a non-empty element count divisible by three;
- reconstructs HASH fields and values;
- parses the third entry of each tuple as a non-negative signed 64-bit absolute expiration timestamp;
- maps `0` to a persistent field;
- rejects malformed tuple counts;
- rejects invalid or negative field TTL values;
- rejects duplicate fields.

Decoded field expirations are restored through SnugKV's native hash field-expiry representation.

## Focused tests

Focused tests cover:

- current type 25 decoding;
- PRE_GA type 23 decoding;
- field/value/TTL triplet restoration;
- persistent fields;
- malformed tuple count rejection;
- negative TTL rejection;
- duplicate field rejection;
- race coverage.

## Live Redis 8.10.2 validation

Redis was reset to normal compact-hash thresholds:

- `hash-max-listpack-entries 512`
- `hash-max-listpack-value 64`

A small three-field hash was created with:

- one persistent field;
- one field expiring at `1790254710657`;
- one field expiring at `1790255010657`.

Redis reported:

`OBJECT ENCODING rdb:hfe:lp -> listpackex`

This confirms the test exercised the LISTPACK_EX path rather than the hashtable HFE metadata path.

After a fresh Redis -> SnugKV full sync, SnugKV restored:

- all three fields and values;
- persistent field `HPEXPIRETIME = -1`;
- short field `HPEXPIRETIME = 1790254710657`;
- later field `HPEXPIRETIME = 1790255010657`;
- positive remaining HPTTL for both expiring fields.

The absolute expiration timestamps matched Redis exactly.

## Replication parser hardening found during this audit

Live testing also exposed a second partial-resynchronization reconnect loop. Redis repeatedly reported successful partial resync of a 14-byte backlog followed by immediate replica disconnect.

The 14-byte payload corresponds to a Redis replication PING:

`*1\r\n$4\r\nPING\r\n`

Root cause:

- `readRedisReplicationCommand` used one `Read` call for the two-byte bulk-string CRLF terminator;
- TCP may legally return only one byte from that read;
- SnugKV then rejected a valid RESP frame and closed the replication connection.

Fix:

- use `io.ReadFull` for the two-byte terminator;
- regression test feeds the RESP frame through a reader that returns one byte at a time.

Focused tests and race tests pass, and the live 14-byte PING reconnect loop no longer reproduces after rebuilding SnugKV.

## Remaining HFE RDB boundaries

Still outside this slice:

- newer Redis hash template RDB types 29-32;
- exact keyspace-level deletion parity when all fields of a hash expire;
- persistence of upstream Redis replication ID/offset across SnugKV process restart.
