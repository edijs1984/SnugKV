# FUNCTION STATS / HELP

SnugKV implements the Redis Functions introspection commands:

```text
FUNCTION STATS
FUNCTION HELP
```

## FUNCTION STATS

The RESP2 reply follows Redis's logical structure:

```text
running_script <nil-or-running-function-map>
engines
  LUA
    libraries_count <integer>
    functions_count <integer>
```

When an FCALL is running, `running_script` contains:

- `name`: the registered function name;
- `command`: the original FCALL/FCALL_RO command vector;
- `duration_ms`: elapsed execution time in milliseconds.

`FUNCTION STATS` is intentionally allowed to bypass SnugKV's normal global
`durableMu`. Writable FCALL execution holds that mutex for atomic command and AOF
semantics, so routing STATS through the ordinary path would make introspection
block until the function had already finished. The bypass is read-only and only
observes a separately synchronized running-function descriptor and the Function
registry counters.

The Lua engine entry reports the number of loaded libraries and globally
registered functions. With no running function, `running_script` is RESP null.

## FUNCTION HELP

`FUNCTION HELP` returns the Redis-style array of Function subcommand help lines,
covering LOAD, DELETE, LIST, STATS, KILL, FLUSH, DUMP, RESTORE, and HELP.

`FUNCTION KILL` is intentionally listed because it is part of Redis's Function
management surface, but it remains a separate implementation milestone in
SnugKV. It requires safe cancellation plus Redis's rule that a function which has
already executed a dataset write is no longer killable; it is not approximated by
an unconditional context cancellation.

## Current remaining Function-management gap

The main remaining management command is `FUNCTION KILL`. `SCRIPT KILL` and
`SCRIPT DEBUG` are tracked separately for the EVAL scripting surface. Broader
Function flags, command-flag/ACL behavior, and Redis-RDB-compatible Function
DUMP bytes also remain compatibility-hardening items.
