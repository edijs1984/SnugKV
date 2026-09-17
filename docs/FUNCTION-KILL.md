# FUNCTION KILL

SnugKV implements `FUNCTION KILL` for currently running Redis Functions invoked through `FCALL` or `FCALL_RO`.

## Behavior

- `FUNCTION KILL` returns `NOTBUSY No scripts in execution right now.` when no function is active.
- A running function that has not crossed a write-command boundary can be cancelled. The command returns `OK` and the active FCALL terminates with a Lua kill error.
- Once the function has dispatched a validated dataset-write command, the invocation becomes unkillable and `FUNCTION KILL` returns Redis-style `UNKILLABLE` semantics.
- `FUNCTION KILL` is handled outside the normal durability serialization mutex, so another client can issue it while the FCALL owns the command-serialization boundary.
- `FCALL_RO` and functions registered with the `no-writes` flag remain killable because their Lua bridge rejects writes before they can cross the dirty-write boundary.

## Write boundary and race handling

Redis marks a running script/function write-dirty when a validated nested command carries the write flag, before command execution. SnugKV follows the same model.

The running-function state mutex serializes the kill transition and the first write transition. Therefore exactly one side wins:

1. If `FUNCTION KILL` marks the invocation killed first, a subsequent nested write is rejected and never dispatched.
2. If the nested write marks the invocation dirty first, `FUNCTION KILL` returns `UNKILLABLE`.

This avoids a race where `FUNCTION KILL` could return `OK` while a write slips through afterward.

## Execution cancellation

Each active FCALL has a private cancellable context layered under the existing five-second function execution timeout. `FUNCTION KILL` cancels that context. The Lua VM observes the cancellation through gopher-lua's context support and unwinds the running function.

The active function entry is removed when FCALL exits, regardless of success, runtime error, timeout, or kill.

## Current boundaries

- SnugKV is single-node and does not implement Redis replication-origin `UNKILLABLE` handling for functions received from a master.
- `SCRIPT KILL` remains a separate unimplemented scripting-management command.
- The current Lua runtime error text is generated through SnugKV/gopher-lua wrapping and is not claimed byte-for-byte identical to every Redis version, although the command-level `NOTBUSY` and `UNKILLABLE` behavior is intentionally aligned.

## Tests

`internal/server/functions_kill_test.go` covers:

- idle `NOTBUSY`,
- cancellation of a real infinite-loop `no-writes` function,
- transition to `UNKILLABLE` after a nested write command,
- preservation of the write performed before that unkillable state,
- `FUNCTION KILL` arity validation.
