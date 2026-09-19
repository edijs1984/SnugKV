# Legacy GEORADIUS compatibility audit

SnugKV implements the legacy Redis GEO radius aliases on top of the same native
ZSET-backed geospatial engine used by GEOSEARCH and GEOSEARCHSTORE.

## Implemented commands

- `GEORADIUS key longitude latitude radius unit [WITHDIST] [WITHHASH] [WITHCOORD] [ASC|DESC] [COUNT count [ANY]] [STORE key] [STOREDIST key]`
- `GEORADIUSBYMEMBER key member radius unit [WITHDIST] [WITHHASH] [WITHCOORD] [ASC|DESC] [COUNT count [ANY]] [STORE key] [STOREDIST key]`

## Redis 8.2 differential coverage

The live wire audit matched Redis 8.2 for:

- radius queries by coordinate and by member;
- ASC/DESC ordering;
- COUNT and COUNT ... ANY;
- WITHDIST / WITHHASH / WITHCOORD reply shapes and ordering;
- STORE preserving geospatial scores;
- STOREDIST storing distance scores in the requested unit;
- last STORE/STOREDIST option winning when both appear;
- missing source key and missing source member behavior;
- WRONGTYPE handling;
- unit, coordinate, radius, COUNT, ANY, syntax, and arity errors;
- STORE incompatibility with WITHDIST/WITHHASH/WITHCOORD;
- dynamic source/destination key discovery for ACL/COMMAND/tracking integration.

## Intentional numeric difference

Stored `STOREDIST` scores can differ from Redis only in the final IEEE-754
rounding digits because SnugKV and Redis do not serialize the intermediate
distance calculation through identical floating-point paths. Examples observed
during the Redis 8.2 audit:

- Redis: `190.44242984775784`
- SnugKV: `190.4424298477578`

- Redis: `586.1049718485142`
- SnugKV: `586.1049718485143`

The user-visible `WITHDIST` replies match Redis's four-decimal legacy formatting,
and the stored numeric differences are far below application-level GEO precision.
