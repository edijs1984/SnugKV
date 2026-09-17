# SCRIPT KILL

SnugKV supports `SCRIPT KILL` for active `EVAL`, `EVALSHA`, `EVAL_RO`, and `EVALSHA_RO` invocations.

The cancellation model follows Redis's dataset-write safety rule:

- if no script is running, `SCRIPT KILL` returns `NOTBUSY No scripts in execution right now.`;
- a running script that has not yet dispatched a writable dataset command can be cancelled;
- after the first writable nested command is dispatched, the invocation becomes unkillable and `SCRIPT KILL` returns `UNKILLABLE ...` rather than interrupting partially-mutated state;
- read-only EVAL variants never cross the dataset-write boundary and remain killable for their full execution.

The first write boundary and `SCRIPT KILL` are serialized through the same running-script state mutex. This closes the race where KILL could otherwise return success while a write starts concurrently.

`SCRIPT KILL` uses a direct introspection/cancellation path and therefore does not wait for the normal global durability mutex held by the running EVAL invocation.

The embedded Lua VM is GopherLua, so stack text produced by a cancelled script is not claimed to be byte-for-byte identical to Redis's Lua VM. The command-level `NOTBUSY`, `UNKILLABLE`, cancellation, and write-safety semantics are the compatibility target.
