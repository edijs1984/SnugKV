# JSON Search Differential Audit

Reference: Redis 8.10 Search.
Harness: `compat/search/search-core.sh`.

## Matching behavior

The audited SnugKV subset matched Redis 8.10 for index creation/list/drop,
result counts, TAG filtering, inclusive NUMERIC ranges, infinite numeric bounds,
implicit AND, mutation visibility, deletion visibility, expiry visibility, and
default JSON result content shape.

The missing-index `FT.DROPINDEX` error is matched as:

```text
SEARCH_INDEX_NOT_FOUND Index not found: <index>
```

## Intentional ordering boundary

Redis returned unsorted hits in internal index order. SnugKV returns ascending
binary key order for deterministic behavior.

Because unsorted Redis Search order is not a stable contract, this remains an
intentional compatibility boundary. `LIMIT` without an explicit future
`SORTBY` can therefore select a different document even when total count and
matching result set are identical.

## Current subset

The current implementation does not yet claim TEXT/scoring, SORTBY, RETURN,
aggregation, GEO/vector search, aliases, cursors, or the full RediSearch query
grammar.
