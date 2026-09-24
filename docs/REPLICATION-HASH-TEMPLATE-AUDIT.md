# Redis Hash Template RDB / Replication Audit

Date: 2026-09-24

## Scope

SnugKV now imports Redis 8.10 hash-template encodings while materializing them as ordinary native HASH values.

Covered Redis RDB formats:

- type 29: `RDB_TYPE_HASH_TMPL_LP` (self-contained DUMP/RESTORE)
- type 30: `RDB_TYPE_HASH_TMPL_LP_REF` (full RDB, registry reference)
- type 31: `RDB_TYPE_HASH_TMPL_ARRAY` (self-contained DUMP/RESTORE)
- type 32: `RDB_TYPE_HASH_TMPL_ARRAY_REF` (full RDB, registry reference)
- opcode 242: `RDB_OPCODE_HASH_TEMPLATE` registry records

## Redis field ordering

Redis template fields use `sdscmplen()`: length first, then byte content. SnugKV validates template fields using the same ordering rule.

Example:

```
age
name
email
```

is valid because the lengths are 3, 4, and 5.

## Full-sync validation

Live Redis 8.10 -> SnugKV diskless full synchronization was validated for both reference encodings.

### Template listpack

Redis reported:

```
OBJECT ENCODING tmpl:lp
"template-listpack"
```

After full sync SnugKV returned the complete logical hash:

```
age   42
email edijs@example.com
name  Edijs
```

and `DBSIZE` was 1 with `master_link_status:up`.

This validates opcode 242 plus type 30 decoding.

### Template array

Redis reported:

```
OBJECT ENCODING tmpl:array
"template-array"
```

After full sync SnugKV returned the complete logical hash and remained connected with `master_link_status:up`.

This validates opcode 242 plus type 32 decoding.

## Live HIMPORT propagation

Redis propagates `HIMPORT SET` as a self-contained `RESTORE` command.

Redis uses `createRawDumpPayload()` for this path, which intentionally writes a zero CRC64 footer. Redis accepts checksum value 0 as "checksum omitted". SnugKV's DUMP verifier now matches that behavior while continuing to verify nonzero CRCs.

Live propagation of a template-array hash was validated:

- Redis created `tmpl:live` using `HIMPORT PREPARE` + `HIMPORT SET`
- Redis reported `template-array`
- SnugKV received the propagated RESTORE
- SnugKV returned all fields/values
- `DBSIZE` advanced to 2
- `master_link_status` remained `up`

This validates the self-contained type 31 live replication path.

The self-contained type 29 decoder is covered by focused tests.

## Replication hardening found during the audit

The audit also exposed and fixed two Redis partial-resynchronization issues:

1. RESP command terminator reads now use `io.ReadFull` so fragmented CRLF does not corrupt a 14-byte replication `PING`.
2. On `+CONTINUE`, SnugKV can recover Redis RESP stream mode from the resumed stream instead of falling into Snug logical-frame parsing. Timeout handling during mode detection no longer falls through into the wrong parser.

## Compatibility boundary

SnugKV does not retain Redis hash-template storage internally. Imported template hashes are materialized as normal SnugKV HASH values. Logical field/value semantics are preserved; Redis template encoding identity is not.

## Verification

Focused coverage includes:

- all four template object forms
- template registry references
- invalid template IDs
- Redis length-first field ordering
- Redis v16 DUMP acceptance
- zero-checksum raw DUMP acceptance
- partial-resync Redis-stream recovery
- fragmented replication command framing
- live full sync for type 30 and type 32
- live RESTORE propagation for template-array
