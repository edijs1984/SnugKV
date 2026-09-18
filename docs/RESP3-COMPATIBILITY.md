# RESP3 Compatibility

SnugKV supports RESP2 and a core RESP3 server surface on the same listener.
Connections start in RESP2 and may switch with `HELLO 3`; `HELLO 2` switches
the same connection back to RESP2.

## Implemented and audited

Redis 8.2 differential audits now cover both the core protocol paths and a broad
command-shape sweep across the implemented surface:

- per-connection protocol state;
- `HELLO`, `HELLO 2`, and `HELLO 3`;
- `HELLO 3 SETNAME <name>` and the tested HELLO option/error surface;
- RESP3 nulls for missing values, including nested nulls such as `MGET`;
- RESP3 maps for `HELLO 3`, `HGETALL`, and `ACL GETUSER`;
- RESP3 sets for `SMEMBERS` and the nested set fields in `COMMAND INFO`;
- RESP3 doubles and nested pair arrays for ZSET score commands, including
  `ZSCORE`, `ZMSCORE`, `ZINCRBY`, `ZRANGE ... WITHSCORES`,
  `ZRANDMEMBER ... WITHSCORES`, and pop replies;
- RESP3 verbatim strings for `INFO` and `CLIENT INFO`;
- nested RESP3 maps/sets used by `COMMAND INFO` key specifications;
- RESP3 GEO coordinate doubles for `GEOPOS` and `GEOSEARCH ... WITHCOORD`;
- RESP3 maps for `XREAD`, `XREADGROUP`, `XINFO STREAM`,
  `XINFO GROUPS`, `XINFO CONSUMERS`, `FUNCTION STATS`, and `CONFIG GET`;
- classic, pattern, and sharded Pub/Sub push frames;
- RESP3 subscribed clients continuing to execute ordinary commands;
- unsubscribe-with-no-active-subscriptions pushes containing RESP3 null;
- `RESET` returning to ordinary command behavior;
- protocol-specific behavior without regressing the existing RESP2 Pub/Sub path.

The final broad structural diff contained no known RESP3 wire-shape defects.
Remaining observed differences were content/environment specific: SnugKV reports
its own server/version/module metadata, Redis may advertise installed modules,
connection IDs naturally differ, and SCAN result ordering/cardinality depends on
the current dataset. During the sweep, an independent Streams semantic gap in
`XINFO GROUPS` entries-read/lag inference was also fixed; that was not a RESP3
framing defect.

## Architecture

RESP2 remains the internal canonical response form for much of the command
implementation. The TCP connection tracks the negotiated protocol and applies a
structural RESP3 adapter where the Redis-visible reply type differs. This keeps
the established RESP2 command behavior stable while allowing command-aware RESP3
maps, sets, doubles, verbatim strings, nulls, and Pub/Sub pushes.

Pub/Sub delivery uses the connection protocol at send time. RESP2 subscribers
retain Redis's restricted subscribed-command mode. RESP3 subscribers receive
push frames and may continue executing ordinary commands while subscriptions
remain active.

## Remaining RESP3 hardening

Core RESP3 support and the broad command-shape differential sweep are complete.
SnugKV still does not claim exhaustive RESP3 protocol parity. Remaining optional
hardening includes:

- supported client-library smoke tests while explicitly using RESP3;
- RESP3 attribute-frame behavior if future implemented commands require it;
- unused RESP3 scalar/container types only when the SnugKV command surface needs
  them;
- continued RESP2 regression coverage as RESP3 breadth expands.

The request parser intentionally continues to accept the normal array-of-bulk
command form used by Redis clients; implementing every possible RESP3 request
type is not currently required for the supported client command path.