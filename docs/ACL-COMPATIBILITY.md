# ACL compatibility

SnugKV implements Redis-style username/password authentication and a substantial
single-node ACL surface. The goal is behavioral compatibility for the commands
and rule forms documented here, while keeping unsupported channel/selector
features explicit.

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

The default user starts enabled with `nopass`, all commands, and all keys unless a
configured ACL file replaces the startup ACL.

## Authentication

`AUTH password` authenticates as `default`. `AUTH username password` selects a
named user. Passwords are stored as SHA-256 hashes and comparisons are performed in
constant time.

A disabled or deleted user is rejected immediately. Existing authenticated
connections are re-checked on every command, so disabling or deleting a user
revokes the session without requiring reconnect.

## Command authorization

Command rules support:

- `+@all` / `-@all`;
- Redis command categories such as `+@read`, `+@write`, `+@string`,
  `-@dangerous`;
- explicit command allow/deny rules such as `+get` and `-set`;
- left-to-right rule application, so later rules override earlier rules.

Category names are case-insensitive. SnugKV uses the Redis 8.2 ACL category table
for category membership and intersects it with commands actually implemented by
SnugKV.

`ACL CAT` returns the Redis 8.2 category list and only reports commands SnugKV
implements for a requested category.

## Key authorization

Key rules support:

- `allkeys`;
- `resetkeys`;
- `~pattern` Redis-style glob patterns.

Authorization reuses the same command-key discovery layer used by
`COMMAND GETKEYS` / `GETKEYSANDFLAGS`. This includes dynamic key extraction for
implemented commands such as EVAL/FCALL, COPY, BITOP, ZSET algebra/store,
ZMPOP/BZMPOP, XREAD, and XREADGROUP.

Commands are rejected before execution if any referenced key is outside the
current user's key patterns.

## Transactions

ACL checks integrate with MULTI/EXEC:

- an ACL denial while queueing marks the transaction dirty;
- EXEC then returns EXECABORT rather than running a partially authorized queue;
- queued commands are authorized again at EXEC time;
- ACL changes made after queueing therefore take effect before execution.

This prevents a user from queueing a command under one rule set and executing it
after permissions have been revoked.

## ACL DRYRUN

`ACL DRYRUN` evaluates command and key authorization without executing the command.
Successful checks return `OK`. Denials use Redis-shaped explanatory bulk strings,
including command and key denials.

## ACL LOG

`ACL LOG` records:

- failed authentication attempts (`reason=auth`);
- command permission failures (`reason=command`);
- key permission failures (`reason=key`).

Entries are newest-first and use the Redis-style ten-field RESP2 record shape:
count, reason, context, object, username, age-seconds, client-info, entry-id,
timestamp-created, and timestamp-last-updated.

Equivalent repeated violations are aggregated by reason/context/object/username.
The original entry ID and creation timestamp are retained, while count,
last-updated timestamp, and client-info are refreshed and the entry moves to the
front. The log is bounded to 128 entries.

`ACL LOG RESET` clears the log.

## ACL persistence

Configure an ACL file in SnugKV's JSON configuration:

```json
{
  "acl_file": "/etc/snugkv/users.acl"
}
```

The equivalent environment variable is:

```text
SNUGKV_ACL_FILE=/etc/snugkv/users.acl
```

When `acl_file` is empty, `ACL SAVE` and `ACL LOAD` return the Redis-compatible
"not configured to use an ACL file" error.

When configured:

- `ACL SAVE` serializes the current users in Redis ACL-file style;
- plaintext passwords are never written; password hashes are emitted as
  `#<sha256>`;
- the file is written through a mode-0600 temporary file and atomic rename;
- `ACL LOAD` parses into a temporary ACL and swaps the live ACL only after the
  complete file validates;
- malformed input leaves the active ACL unchanged;
- startup loads the configured ACL file before accepting clients;
- a missing or malformed configured ACL file fails startup closed.

Example persisted users:

```text
user default on nopass sanitize-payload ~* &* +@all
user app on sanitize-payload #<sha256> ~app:* &* -@all +@read +set
```

## Differential validation

The implemented ACL surface has been compared live with Redis 8.2 for:

- AUTH success and failure behavior;
- WHOAMI / USERS / GETUSER / LIST / SETUSER / DELUSER;
- DRYRUN and GENPASS;
- CAT category enumeration and category command sets;
- ordered category/command overrides;
- key-pattern enforcement;
- ACL failures inside MULTI and authorization changes before EXEC;
- LOG entry structure, ordering, RESET, and repeated-violation aggregation;
- SAVE / LOAD behavior with and without `aclfile`;
- persisted password hashes and startup restoration;
- malformed ACL-file atomicity/fail-closed startup behavior.

Runtime-specific fields in `ACL LOG client-info`, timestamps, and entry IDs are
naturally instance-specific rather than byte-identical.

## Current boundaries

SnugKV does not yet claim full Redis ACL parity. Remaining work includes:

- channel-pattern enforcement (`&pattern`, `allchannels`, `resetchannels`);
- ACL selectors and selector serialization;
- less-common SETUSER reset/removal modifiers such as password-hash removal;
- exact channel/selector presentation in GETUSER/LIST for non-default users;
- deeper ACL interaction auditing for dynamically resolved SORT BY/GET keys and
  scripting/function command-policy edge cases.

These gaps are compatibility boundaries, not bypasses of the implemented command
and key ACL checks.
