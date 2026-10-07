# LIST throughput audit: RPUSH on indexed lists

## Problem

Lists with 32 or more elements use the indexed SL2 layout. When an `RPUSH`
exhausted the reserved offset or payload headroom, `indexedListAppend` decoded
every element (one allocation each), appended, and re-encoded the whole list.
Push cost therefore grew with list length.

## Change

When headroom is exhausted, the list is now grown by copying the stored offset
table and payload verbatim into a larger buffer (`growIndexedListRaw`). The
decode/re-encode path remains as the fallback when the payload has unreferenced
space or fails validation, so corruption is still reported by the same path.

## Verification

- `TestListPushRightGrowthMatchesReference`: seeded randomized single and batch
  pushes (empty values, multi-byte length varints, promotion at 32 elements)
  against a slice reference, with `LINDEX` probes, periodic full `LRANGE`
  comparison and `LPOP`/`RPOP` afterwards.
- `TestGrowIndexedListRawPreservesElementsAndOrder`.
- `TestGrowIndexedListRawDeclinesWhenPayloadHasDeadSpace`.
- Operator ran the tests locally: pass.

## Engine-level benchmark (`BenchmarkListPushRightSequential`, 64 B values)

| Elements per list | Before ns/op | After ns/op | Allocs before -> after |
|---|---|---|---|
| 10 | unchanged | unchanged | unchanged |
| 100 | 1,199-1,271 | 925-1,089 | 6 -> 1 |
| 1000 | 1,314-1,481 | 670-772 | 9 -> 0 |

## Live Redis vs SnugKV

Pending: list-small, list-medium, list-large on Redis and snug-opt.

## Remaining gaps

- Lists under 32 elements (SL1) rewrite the whole blob per push (~1 us).
- list-small `LINDEX` gap vs Redis is unproven; needs isolated measurement.
