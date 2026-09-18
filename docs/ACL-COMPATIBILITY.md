# ACL compatibility

SnugKV implements Redis-style username/password authentication and the audited
single-node ACL surface for the commands and rule forms documented here.

## Implemented commands

```text
AUTH [username] password

ACL WHOAMI
ACL USERS
ACL GETUSER username
ACL LIST
ACL SETUSER username [rule ...]
ACL DELUSER username [username ...]
ACL CAT [category]
ACL DRYRUN username command [arg ...]
ACL GENPASS [bits]
ACL LOG [count|RESET]
ACL SAVE
ACL LOAD
ACL HELP
```

The default user starts enabled with `nopass`, all commands, all keys, and all
channels unless a configured ACL file replaces the startup ACL. Newly created
users start disabled, with `-@all`, no keys, no channels, and
`sanitize-payload`.

## Authentication

`AUTH password` authenticates as `default`. `AUTH username password` selects a
named user. Passwords are stored as SHA-256 hashes and comparisons are performed
in constant time.

A disabled or deleted user is rejected immediately. Existing authenticated
connections are re-checked on every command, so disabling or deleting a user
revokes the session without requiring reconnect.

## Command authorization

Command rules support explicit command allow/deny rules, Redis 8.2 command
categories, `+@all` / `-@all`, and the `allcommands` / `nocommands`
aliases. Rules are applied left-to-right.

SnugKV uses the Redis 8.2 ACL category table for category membership and
intersects it with commands actually implemented by SnugKV.

## Key authorization

Key rules support `allkeys`, `resetkeys`, and `~pattern`. Authorization
reuses the same command-key discovery layer used by `COMMAND GETKEYS` /
`GETKEYSANDFLAGS`, including implemented dynamic-key commands such as
EVAL/FCALL, COPY, BITOP, ZSET algebra/store, ZMPOP/BZMPOP, XREAD, and XREADGROUP.

A complete matching ACL rule set must permit every referenced key.

Nested `redis.call()` / `redis.pcall()` execution inside EVAL/EVALSHA,
EVAL_RO/EVALSHA_RO, FCALL, and FCALL_RO is re-authorized using the authenticated
caller's ACL context. Scripts and Functions therefore cannot use nested commands
to bypass command or key restrictions.

Redis 8.2 applies a stricter rule to wildcard external `SORT BY` / `GET`
patterns: the authorizing root rule set or selector must grant full key scope.
Matching only the source key and the concrete derived key-pattern namespace is
not sufficient. SnugKV matches that behavior.

## Channel authorization

Channel rules support `allchannels`, `resetchannels`, and `&pattern`.
Classic and sharded Pub/Sub enforcement covers `PUBLISH`, `SUBSCRIBE`,
`PSUBSCRIBE`, `SPUBLISH`, and `SSUBSCRIBE`.

For ordinary channel access, Redis-style glob matching is used. For
`PSUBSCRIBE`, the requested subscription pattern must exactly equal an allowed
ACL channel pattern unless all channels are allowed. A bare `&` is accepted as
an empty channel pattern, matching the audited Redis 8.2 behavior.

## Selectors

ACL selectors are supported and serialized in `ACL GETUSER`, `ACL LIST`, and
ACL files.

Authorization follows Redis's rule-set model:

```text
root rule set fully matches
OR
selector 1 fully matches
OR
selector 2 fully matches
...
```

Command, key, and channel permissions from different selectors are not mixed. One
complete root/selector rule set must authorize the command and all of its
key/channel arguments.

Empty selectors `()` and whitespace-only selectors are valid restrictive
selectors. Password, user-state, reset, and sanitize-payload modifiers are not
valid inside selectors. Redis 8.2 does not expose a `resetselectors` modifier in
the audited surface; `reset` clears selectors.

## SETUSER modifiers

The audited ordinary SETUSER surface includes:

- `reset`, `on`, `off`
- `nopass`, `resetpass`
- `>password` and `<password`
- `#<sha256>` and `!<sha256>`
- exact 64-character lowercase hexadecimal hash validation
- `allcommands`, `nocommands`, `+@all`, `-@all`
- category and explicit command rules
- `allkeys`, `resetkeys`, `~pattern`
- `allchannels`, `resetchannels`, `&pattern`
- `sanitize-payload`, `skip-sanitize-payload`
- selectors

`nopass` clears stored password hashes. `resetpass` clears hashes and disables
`nopass`. `reset` restores fresh-user ACL state and clears selectors. Modifier
ordering is significant and applied left-to-right.

## Transactions

ACL checks integrate with MULTI/EXEC:

- a denial while queueing marks the transaction dirty;
- EXEC returns EXECABORT rather than running a partially authorized queue;
- queued commands are authorized again at EXEC time;
- ACL changes after queueing therefore take effect before execution.

## ACL DRYRUN

`ACL DRYRUN` evaluates the same command/key/channel/selector rule-set semantics
without executing the command. Successful checks return `OK`; denials return
Redis-shaped explanatory bulk strings.

## ACL LOG

`ACL LOG` records authentication, command, key, and channel denials. Entries are
newest-first, repeated equivalent violations are aggregated, and the log is
bounded to 128 entries.

## ACL persistence

When `acl_file` is configured, `ACL SAVE` serializes Redis-style user rules
including hashed passwords, channel rules, sanitize flags, and selectors.
`ACL LOAD` parses into a temporary ACL and replaces the live ACL only after the
complete file validates. Startup restores the configured ACL before serving
clients; missing or malformed configured ACL files fail startup closed.

## Differential validation

Direct Redis 8.2 differential testing covers:

- AUTH and user-management replies/errors;
- command/category/key authorization and ordering;
- Pub/Sub channel patterns, allchannels/resetchannels, classic and sharded paths;
- SETUSER reset/password/hash/alias/sanitize modifier behavior and errors;
- root-or-selector authorization, multiple selectors, selector categories, keys,
  channels, malformed selectors, GETUSER/LIST serialization, and DRYRUN;
- MULTI queue-time dirtying and EXEC-time re-authorization;
- ACL LOG aggregation and channel-denial logging;
- SAVE/LOAD, password-hash persistence, restart restoration, and fail-closed
  malformed ACL startup;
- dynamic scripting/Function ACL enforcement and wildcard external SORT BY/GET
  policy. See `docs/DYNAMIC-ACL-DIFFERENTIAL-AUDIT.md`.

## Remaining hardening

There is no known core feature gap in the documented single-node ACL surface.
Optional deeper audits remain useful for dynamically resolved SORT BY/GET external
keys and unusual scripting/Function policy interactions.