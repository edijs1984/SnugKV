# Dynamic ACL Differential Audit

This audit compares SnugKV against Redis 8.2 for ACL behavior that is not fully
described by static command-key metadata: nested Lua/Function command execution
and external SORT BY/GET key patterns.

## Scope

The live harness is:

```text
compat/acl/dynamic-scripting-sort.sh
```

It was run against:

- SnugKV on port 6380
- Redis 8.2 on port 6390

## Audited cases

The following behaviors match Redis 8.2 semantically:

- EVAL cannot use redis.call() to invoke a command denied to the authenticated user.
- EVAL cannot use redis.call() to access a key denied to the authenticated user.
- EVAL_RO applies the same nested ACL enforcement.
- FCALL inherits the caller's ACL and cannot use redis.call() to bypass key rules.
- SORT with external BY patterns is denied unless the authorizing root rule set
  or selector grants full key scope.
- A selector that lists only the source and resolved BY/GET key patterns is still
  denied for wildcard external SORT patterns, matching Redis 8.2.
- SORT with external BY/GET patterns succeeds when one complete authorizing rule
  set grants the command and all keys.
- redis.call('SORT', ...) inside Lua preserves the nested SORT ACL context and
  applies the same external-pattern restriction.

Redis 8.2 requires full key-read scope for wildcard external SORT BY/GET access.
SnugKV now applies that rule per complete root/selector rule set instead of
combining permissions across selectors.

## Intentional formatting differences

The allow/deny decisions and Redis-specific SORT ACL error text match. Lua and
Function failures retain implementation-specific wrapper text:

- Redis reports `ACL failure in script` plus Redis script/function source metadata.
- SnugKV reports the underlying NOPERM/ERR through GopherLua runtime/stack text.

This is treated as a VM/runtime formatting difference rather than a semantic ACL
difference. SnugKV does not attempt to fake Redis's Lua source-location strings.

## Validation

Focused regression tests cover:

- nested command denial;
- nested key denial;
- read-only script denial;
- Function nested-key denial;
- split-selector SORT denial;
- partial-pattern SORT denial;
- all-key selector SORT success;
- nested SORT denial inside Lua.

The final branch must also pass the standard race, vet, and RESP fuzz gates before
merge.
