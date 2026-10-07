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

Operator run on `perf/list-memory` (1M items, 64 B values, 8 workers, pipeline 256,
Intel i3-7020U, Redis vs SnugKV; list-small was measured on the earlier branch):

| Profile | Metric | Redis | SnugKV | Delta |
|---|---|---|---|---|
| list-medium (100/key) | load ops/s | 508,620 | 503,907 | -1% |
| | read ops/s | 387,268 | 605,241 | +56% |
| | read p99 us | 33.3 | 33.4 | parity |
| | settled bytes/item | 72.3 | 76.8 | +6% (was +15%) |
| list-large (1000/key) | load ops/s | 510,524 | 568,304 | +11% |
| | read ops/s | 417,358 | 607,881 | +46% |
| | read p99 us | 27.6 | 31.3 | +13% |
| | settled bytes/item | 67.8 | 76.9 | +13% (was +28%) |

Settled bytes/item before the trim change: medium 83.0, large 86.5.

Findings from the diagnostics export:

- Arena internal waste is 6.6 MB (medium) and 7.8 MB (large) of ~76 MB: blobs
  over 8 KB round up to the next ~12.5% size class. This is the remaining
  settled-memory gap and needs finer large size classes in the arena.
- Memory right after load (before the optimizer converges) is 98.6 B/item
  (medium) and 194 B/item (large); process peak RSS reached 431 MB on
  list-large. Each regrow leaves a dead copy in the arena and allocates a
  temporary heap buffer.
- Regrow headroom is now doubled (capped at 1 MiB, like Redis SDS preallocation).
  Engine-level, 1000 items/key: push 553 -> 374 ns/op, 702 -> 322 B/op, memory
  right after load 385 -> 156 B/item; settled size is unchanged because idle
  lists are trimmed.

## Remaining gaps

- Lists under 32 elements (SL1) rewrite the whole blob per push (~1 us).
- list-small `LINDEX` gap vs Redis is unproven; needs isolated measurement.
