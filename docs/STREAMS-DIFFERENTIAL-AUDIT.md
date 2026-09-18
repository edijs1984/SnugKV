# Streams Redis 8.2 Differential Audit

Status: **complete** (2026-09-18)

This document records the live Redis 8.2 differential audit for SnugKV's
implemented Streams and consumer-group surface. Redis was used as the behavioral
oracle and SnugKV was compared over RESP against the same deterministic command
sequences.

The implementation changes from this audit landed in commit
`71d863168aadccc0598e4cc4260226de6f694018`
(`fix(streams): complete Redis 8.2 differential parity fixes`).

## Scope

The audit covered:

- stream ID handling, including explicit IDs, `*`, `ms-*`, duplicate/lower IDs,
  malformed IDs, and the `0-0` boundary;
- `XRANGE` / `XREVRANGE` inclusive and exclusive bounds, bare-millisecond
  bounds, COUNT behavior, and malformed ranges;
- `XTRIM` and XADD trimming with `MAXLEN`, `MINID`, exact/approximate syntax,
  LIMIT grammar, and lifetime metadata;
- consumer-group creation and repositioning through `XGROUP CREATE` /
  `SETID`, including `$` and `ENTRIESREAD`;
- `XREADGROUP`, `XACK`, `XPENDING`, consumer creation/deletion, PEL cleanup,
  and lag/entries-read reporting;
- `XCLAIM` / `XAUTOCLAIM`, including min-idle filtering, retry counts,
  `JUSTID`, and deleted-PEL cleanup;
- `XINFO STREAM`, `GROUPS`, `CONSUMERS`, and `STREAM FULL`;
- Redis 8.2 reference policies `KEEPREF`, `DELREF`, and `ACKED` across
  `XDELEX`, `XACKDEL`, `XTRIM`, and XADD trimming;
- WRONGTYPE, missing-key/group, syntax, arity, and Redis error-class behavior.

## Pass 1 — claims, pending entries, and XINFO

The first pass matched Redis for the tested semantic surface:

- `XREADGROUP`
- `XPENDING` summary/detail
- `XCLAIM` with `RETRYCOUNT` and `JUSTID`
- min-idle filtering
- `XAUTOCLAIM`
- `XAUTOCLAIM JUSTID`
- deleted pending-entry cleanup
- `XINFO GROUPS`
- `XINFO CONSUMERS`
- `XINFO STREAM FULL`

No semantic diff remained in this pass.

## Pass 2 — Redis 8.2 reference policies

The second pass covered `KEEPREF`, `DELREF`, and `ACKED` across explicit
deletion, acknowledgement+deletion, trimming, and XADD trimming.

The audit found and fixed two real compatibility defects:

1. `XACKDEL ... DELREF` now returns the Redis-compatible per-ID success status
   when the command removes a dangling PEL reference even if the stream entry was
   already absent.
2. Trimming no longer advances `max-deleted-entry-id`. Redis keeps this metadata
   unchanged for trim removal and advances it for explicit deletion.

After the fixes, the semantic output matched Redis for the audited reference-policy
surface.

## Pass 3 — IDs, trimming grammar, groups, consumers, and errors

The third pass found and fixed the following parity issues:

- `XRANGE ... COUNT 0` returns a null array as Redis does;
- `XTRIM ... LIMIT` is rejected unless the special `~` form is used;
- implicit consumer-group `entries-read` remains unknown/null while lag may
  still be inferable;
- `ENTRIESREAD -1` is accepted;
- `XGROUP DELCONSUMER` removes that consumer's pending entries from the PEL;
- manually created never-active consumers report `inactive = -1`;
- empty `XPENDING` summary uses Redis's null fourth field;
- `BUSYGROUP` and `NOGROUP` retain their Redis error classes instead of being
  rewritten as generic `ERR`;
- negative `XTRIM ... MAXLEN` matches Redis's tested error wording.

The focused engine/server race tests remained green after these changes.

## Intentional implementation-specific differences

Two observed differences are not treated as compatibility defects.

### Approximate `XTRIM ~` granularity

Redis's approximate trimming behavior is coupled to its internal stream
radix-tree/listpack node layout. SnugKV stores Streams in its own packed logical
representation and therefore does not promise the same number of entries removed
by a particular `~` + LIMIT invocation.

The grammar and safety semantics are supported, but the exact approximate removal
count is implementation-specific.

### `radix-tree-keys` / `radix-tree-nodes`

These `XINFO STREAM` fields describe Redis's internal radix-tree representation.
SnugKV does not use Redis's radix tree and does not fabricate Redis-internal node
counts merely to make diagnostic metadata byte-identical.

Application-visible stream contents, IDs, groups, PEL behavior, lifetime metadata,
and the audited error semantics remain the compatibility target.

## Result

For the documented single-node Streams surface, the Redis 8.2 differential
edge-case audit is complete.

There is no known core Streams command-family compatibility gap from this audit.
Future Streams work should be driven by concrete client/workload requirements or
newly discovered Redis behavior rather than by the completed roadmap item.
