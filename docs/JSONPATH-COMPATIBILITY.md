# JSONPath Compatibility

SnugKV implements first-class JSON values and a broad Redis-compatible JSONPath surface.

## Oracle

The differential audit for this milestone used Redis 8.10.2 as the reference implementation. The live harness is:

```text
compat/json/jsonpath-extended.sh
```

The audited server behavior was exercised against Redis on one port and SnugKV on another, then compared from raw `redis-cli --raw` output.

## Audited JSONPath surface

The current implementation covers:

- root/member/index paths, including bracket-member syntax;
- object and array wildcards;
- recursive member descent and recursive wildcard descent;
- positive-step array slices and negative array indices;
- array unions, preserving repeated union entries for read queries;
- scalar comparisons: `< <= > >= == !=`;
- logical filters: `&& || !` and parentheses;
- regular-expression matching with `=~`;
- membership operators `in` and `nin`;
- set relations `subsetof`, `anyof`, and `noneof`;
- `size`, `sizeof`, and `empty`;
- arithmetic expressions `+ - * / %` with unary `+`/`-`;
- functions including `length`, `abs`, `ceiling`, `floor`, `first`, `last`, `index`, `min`, `max`, `sum`, `avg`, `stddev`, `append`, `keys`, `count`, and `value`;
- multi-match `JSON.GET`, `JSON.TYPE`, `JSON.DEL`, and updates to existing matched locations with `JSON.SET`;
- no-match behavior for audited read/type queries;
- AOF/restart persistence of JSON values and JSONPath-visible mutations.

Redis 8.10 rejects negative slice steps such as `[::-1]`; SnugKV matches that behavior.

Redis 8.10 preserves repeated union selections such as `[0,0,2]` for reads; SnugKV matches that behavior while mutation paths internally avoid applying the same array mutation twice.

Redis 8.10 does not allow a dynamic selector path such as a filter to create a previously missing terminal member with `JSON.SET`. SnugKV matches that behavior and returns `ERR wrong static path` for the audited case. Static paths may still create a missing terminal member.

## Known compatibility boundaries

### Object member order

SnugKV currently represents decoded JSON objects as Go maps. Source insertion order is therefore not retained.

Consequences:

- encoded object members may be returned in a different order than Redis;
- wildcard object traversal can differ in result order;
- recursive wildcard `$..*` can differ in traversal order even when the selected value set is equivalent.

This is a representation-level boundary, not a selector correctness issue. Fixing it would require an ordered-object representation and is intentionally outside this milestone.

### Error text

For invalid JSONPath syntax and some static-path failures, SnugKV returns the same failure class but does not guarantee Redis 8.10's exact byte-for-byte parser message. Examples include negative slice-step syntax and static-path diagnostics.

Compatibility claims for this milestone are semantic unless exact wording is explicitly covered by a test.

### Redis TYPE vs SnugKV semantic type

For Redis command compatibility, `TYPE <json-key>` returns `string`. SnugKV retains an internal semantic JSON tag, available through `SNUG.TYPE <key>`, which returns `JSON`.

## Persistence

AOF restart coverage verifies that:

- a JSON document is journaled and restored;
- JSONPath-visible mutations survive restart;
- JSON queries continue to operate after replay;
- Redis-visible `TYPE` remains `string`;
- `SNUG.TYPE` remains `JSON`.

## Scope

This document records the audited standalone JSONPath behavior. It does not claim byte-identical JSON serialization ordering, byte-identical parser errors, or exhaustive compatibility for every RedisJSON/Redis 8.10 expression outside the tested surface.
