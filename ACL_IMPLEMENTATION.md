# SnugKV ACL / AUTH v1

Implement Redis 8.2 compatible authentication and ACL support.

## Scope

Implement:

- AUTH <password>
- AUTH <username> <password>

- ACL WHOAMI
- ACL USERS
- ACL LIST
- ACL GETUSER <username>
- ACL SETUSER <username> [rules...]
- ACL DELUSER <username> [...]
- ACL HELP

## Required ACL rules

Support:

- on
- off
- nopass
- resetpass
- >password

- allkeys
- resetkeys
- ~pattern

- +command
- -command

- +acl|whoami

- +@all
- -@all

Passwords must be stored as SHA-256 hashes, not plaintext.

## Default user

On startup create:

user default on nopass sanitize-payload ~* &* +@all

A connection should initially behave as authenticated as `default`
while the default user is `nopass`.

If the default user has a password, new unauthenticated connections must
return:

NOAUTH Authentication required.

## Architecture

Do not put ACL logic in internal/engine.

Create:

internal/server/acl.go
internal/server/acl_test.go
internal/server/acl_wire_test.go

ACL state belongs to Server.

Per-connection authentication state belongs in tcp_server.go.

Suggested:

type authSession struct {
    username      string
    authenticated bool
}

## Command pipeline

Authorization must happen before command execution:

RESP decode
-> AUTH handling
-> authentication / NOAUTH gate
-> command permission check
-> key permission check
-> CLIENT / transaction / pubsub / normal execution

AUTH itself must remain callable while unauthenticated.

## Transaction security

ACL must not be bypassable through MULTI/EXEC.

Example:

restricted user
MULTI
SET forbidden:key value
EXEC

The SET must not execute.

Authorization should be checked before the forbidden command is accepted
into the transaction queue, matching Redis behavior as closely as possible.

Also ensure EXEC cannot execute a command that is no longer authorized
if ACL rules changed after queueing.

## Command permission names

Normal:

get
set
ping

ACL subcommands:

acl|whoami
acl|users
acl|list
acl|getuser
acl|setuser
acl|deluser
acl|help

## Key authorization

Use command key metadata where possible.

For v1 at minimum ensure:

GET
SET
DEL
EXISTS
MGET
MSET
MSETNX
GETSET
GETDEL
GETEX
SETNX
SETEX
PSETEX
INCR*
DECR*
APPEND
STRLEN
EXPIRE*
TTL
PTTL
PERSIST
RENAME
RENAMENX
TOUCH
UNLINK

respect ACL key patterns.

Multiple-key commands must verify every accessed key.

Glob patterns such as:

~allowed:*

must work.

Denied key:

NOPERM No permissions to access a key

## Redis-compatible responses

Bad user/password or disabled user:

WRONGPASS invalid username-password pair or user is disabled.

Unauthenticated:

NOAUTH Authentication required.

Command denied:

NOPERM User snugtest has no permissions to run the 'set' command

ACL subcommand denied:

NOPERM User snugtest has no permissions to run the 'acl|whoami' command

Key denied:

NOPERM No permissions to access a key

AUTH <password> when default user has nopass:

ERR AUTH <password> called without any password configured for the default user. Are you sure your configuration is correct?

AUTH wrong arity:

ERR wrong number of arguments for 'auth' command

AUTH with 3+ arguments must match Redis audit behavior:

ERR syntax error

ACL with no subcommand:

ERR wrong number of arguments for 'acl' command

Unknown ACL command:

ERR unknown subcommand 'WHATEVER'. Try ACL HELP.

## New users

ACL SETUSER newuser

creates:

flags:
off
sanitize-payload

passwords:
empty

commands:
-@all

keys:
empty

channels:
empty

selectors:
empty

## ACL GETUSER

Default output must match Redis RESP structure so redis-cli prints:

flags
on
nopass
sanitize-payload
passwords

commands
+@all
keys
~*
channels
&*
selectors

For configured users, password hashes must be emitted without # in GETUSER.

## ACL LIST

Default:

user default on nopass sanitize-payload ~* &* +@all

Configured user example:

user snugtest on sanitize-payload #<sha256> ~* resetchannels -@all +get

Keep output deterministic.

## ACL USERS

Return usernames in deterministic Redis-compatible ordering.

## ACL DELUSER

Return integer number of users actually deleted.

Do not allow deleting `default` if that would leave SnugKV in an invalid
authentication state unless Redis behavior has been explicitly audited.

## Error response support

tcp_server.go errorResponse currently only preserves selected prefixes.

Add support so these are emitted without being rewritten to ERR:

NOAUTH
WRONGPASS
NOPERM

For example:

-NOAUTH Authentication required.\r\n
-WRONGPASS invalid username-password pair or user is disabled.\r\n
-NOPERM User snugtest has no permissions to run the 'set' command\r\n

## COMMAND metadata

Add AUTH and ACL to command metadata / COMMAND INFO / docs.

AUTH:
- no keys
- connection/security command

ACL:
- no data keys
- admin/security command

Do not expose passwords in logs or metrics.

## Concurrency

ACL user state may be read by every connection and mutated by ACL SETUSER /
DELUSER.

Protect it with an RWMutex or equivalent.

Do not hold ACL locks while executing store commands.

## Tests

Add unit and wire tests for:

1. default user nopass
2. AUTH password form
3. AUTH username/password
4. wrong credentials
5. disabled user
6. WHOAMI
7. USERS
8. LIST
9. GETUSER
10. missing GETUSER
11. SETUSER
12. DELUSER
13. +get / denied SET
14. +acl|whoami
15. resetkeys
16. ~allowed:*
17. denied key
18. default user password
19. NOAUTH
20. restore nopass
21. arity errors
22. unknown ACL subcommand
23. concurrent ACL reads/mutations
24. MULTI ACL bypass attempts
25. multiple-key permission checks

## Required validation

Run:

go test -race -count=1 ./internal/server
go test -race -count=1 ./...
go vet ./...

Then start SnugKV and run:

/tmp/audit-acl-v2.sh | tee /tmp/audit-acl-v2.txt

Compare Redis :6390 and SnugKV :6380.

Do not mark ACL complete until the v2 audit matches for the implemented
surface.

## Do not implement yet

Leave these for ACL v2 unless naturally required:

ACL CAT
ACL DRYRUN
ACL GENPASS
ACL LOAD
ACL SAVE
ACL LOG
selectors
channel permissions
full Redis command category taxonomy

The current milestone is authentication + basic users + command/key
authorization.
