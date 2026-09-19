# Redis DUMP / RESTORE Compatibility

SnugKV implements Redis-compatible key-level `DUMP` and `RESTORE` for the
audited single-node Redis 8.2 surface.

## Supported object families

The implementation reads and writes Redis RDB object payloads for:

- STRING, including Redis integer encodings and LZF-compressed strings;
- HyperLogLog, via Redis-compatible STRING serialization;
- HASH using `RDB_TYPE_HASH_LISTPACK`;
- SET using `RDB_TYPE_SET_INTSET` and `RDB_TYPE_SET_LISTPACK`;
- LIST using `RDB_TYPE_LIST_QUICKLIST_2`;
- ZSET using `RDB_TYPE_ZSET_LISTPACK`;
- STREAM using `RDB_TYPE_STREAM_LISTPACKS_3`.

Payloads use Redis RDB version 12 and the Redis CRC64 trailer.

## RESTORE behavior

The audited command behavior includes:

- relative TTL and `ABSTTL`;
- `REPLACE`;
- Redis `BUSYKEY` behavior;
- checksum/version rejection;
- binary-safe payloads;
- `IDLETIME` / `FREQ` syntax and validation parity for the tested cases.

SnugKV validates the payload before publishing the restored logical object.
Native containers are reconstructed atomically through the logical persistence
record path.

## Live Redis 8.2 interoperability

The compatibility harnesses under `compat/keyspace/` verify both directions.

Byte-identical audited output was observed for the shared fixtures for:

- plain, integer-encoded, LZF-compressed, and binary STRING values;
- HASH listpack;
- SET listpack;
- SET intset;
- LIST quicklist2;
- ZSET listpack;
- HyperLogLog STRING;
- STREAM with live entries and no deleted tombstones.

Redis `DUMP` payloads for those fixtures restore successfully in SnugKV, and
SnugKV `DUMP` payloads restore successfully in Redis.

STREAM cross-restore also preserves:

- `last-generated-id`;
- `max-deleted-entry-id`;
- `entries-added`;
- consumer groups;
- consumer ownership;
- pending-entry lists;
- delivery counters and timestamps.

## STREAM tombstone boundary

Redis retains deleted stream entries as tombstones inside its stream listpack.
SnugKV removes deleted field/value payloads from its native stream representation
while preserving Redis-visible lifetime metadata such as
`max-deleted-entry-id` and `entries-added`.

Therefore:

- Redis STREAM payloads containing deleted tombstones restore semantically into
  SnugKV;
- SnugKV preserves the live entries, lifetime metadata, groups, consumers, and
  PEL state;
- a later SnugKV `DUMP` cannot reproduce Redis's original deleted field/value
  tombstone bytes exactly.

This is a storage-model boundary, not a checksum or RDB decoder limitation.

Redis-internal stream radix-tree diagnostics are also not fabricated for exact
metadata parity.

## Audit harnesses

- `compat/keyspace/dump-restore-wire.py`
- `compat/keyspace/dump-restore-cross.py`
- `compat/keyspace/dump-native-types-wire.py`
- `compat/keyspace/dump-native-cross.py`
- `compat/keyspace/dump-stream-wire.py`
- `compat/keyspace/dump-stream-cross.py`

