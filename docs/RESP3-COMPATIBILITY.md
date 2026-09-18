# RESP3 Compatibility

SnugKV supports RESP2 and a core RESP3 server surface on the same listener.
Connections start in RESP2 and may switch with `HELLO 3`; `HELLO 2` switches
the same connection back to RESP2.

## Implemented and audited

The current Redis 8.2 differential audit covers:

- per-connection protocol state;
- `HELLO`, `HELLO 2`, and `HELLO 3`;
- `HELLO 3 SETNAME <name>` and the tested HELLO option/error surface;
- RESP3 nulls for missing values, including nested nulls such as `MGET`;
- RESP3 maps for `HELLO 3`, `HGETALL`, and `ACL GETUSER`;
- RESP3 sets for `SMEMBERS` and the nested set fields in `COMMAND INFO`;
- RESP3 doubles and nested pair arrays for `ZRANGE ... WITHSCORES`;
- RESP3 verbatim strings for `INFO`;
- nested RESP3 maps/sets used by `COMMAND INFO` key specifications;
- classic, pattern, and sharded Pub/Sub push frames;
- RESP3 subscribed clients continuing to execute ordinary commands;
- unsubscribe-with-no-active-subscriptions pushes containing RESP3 null;
- `RESET` returning to ordinary command behavior;
- protocol-specific behavior without regressing the existing RESP2 Pub/Sub path.

The audited Redis-vs-SnugKV differences were content-specific rather than RESP3
wire-shape defects: SnugKV reports its own server/version/module metadata and
INFO body, connection IDs naturally differ, and SCAN ordering is implementation
dependent.

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

Core RESP3 support is implemented, but SnugKV does not claim exhaustive RESP3
parity yet. Remaining work includes:

- a broader command-by-command Redis differential sweep for less common reply
  shapes;
- supported client-library smoke tests while explicitly using RESP3;
- RESP3 attribute-frame behavior if future implemented commands require it;
- unused RESP3 scalar/container types only when the SnugKV command surface needs
  them;
- continued RESP2 regression coverage as RESP3 breadth expands.

The request parser intentionally continues to accept the normal array-of-bulk
command form used by Redis clients; implementing every possible RESP3 request
type is not currently required for the supported client command path.
