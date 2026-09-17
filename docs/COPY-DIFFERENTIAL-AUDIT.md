# COPY Redis Differential Audit

Date: 2026-09-17

Comparison environment:

- Redis on port `6379`
- SnugKV on port `6380`
- SnugKV command surface: `COPY source destination [DB 0] [REPLACE]`

## Result

The implemented DB0 COPY surface matched Redis for the tested behaviors below. The only observed difference was `COPY ... DB 1`: Redis accepted it because the reference Redis instance exposes multiple logical databases, while SnugKV intentionally exposes only DB 0 and returned `ERR DB index is out of range`.

That DB1 difference is the documented topology boundary, not a COPY semantic defect.

## Matched behavior

- existing source copied to a new destination returns `1`;
- source remains unchanged;
- destination receives the same string value;
- existing destination without `REPLACE` returns `0`;
- `REPLACE` returns `1` and overwrites the destination;
- missing source returns `0`;
- identical source/destination is rejected with `ERR source and destination objects are the same`;
- `DB 0` succeeds;
- non-integer DB input returns `ERR value is not an integer or out of range`;
- unknown options return `ERR syntax error`;
- source TTL is copied to the destination as the same absolute expiry, subject only to normal command-execution timing drift;
- LIST, SET, and ZSET datatypes are preserved;
- ZSET scores and ordering match Redis for the tested data;
- `REPLACE` can change the destination datatype, including STRING to LIST;
- copied native container values are independent deep copies: mutating the source afterward does not mutate the destination.

## Observed TTL sample

Redis:

- source PTTL: `59974`
- destination PTTL: `59970`

SnugKV:

- source PTTL: `59976`
- destination PTTL: `59969`

The small deltas are normal command timing. Both servers preserved the same logical expiry relationship between source and destination.

## Documented boundary

Redis reference result:

```text
COPY copy:src copy:db1 DB 1
(integer) 1
```

SnugKV result:

```text
COPY copy:src copy:db1 DB 1
(error) ERR DB index is out of range
```

SnugKV is intentionally a single-database server. Cross-database COPY remains outside the current compatibility target and is tracked together with broader migration/transfer scope.

## Conclusion

The direct Redis-vs-SnugKV audit for COPY option, error, TTL, datatype, REPLACE, and deep-copy behavior is complete for SnugKV's documented DB0 compatibility surface.
