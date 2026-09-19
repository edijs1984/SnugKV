# SCRIPT DEBUG full LDB implementation

This document records the implemented Redis 8.2 LDB design, the controlled
GopherLua VM extension used by SnugKV, and the differential acceptance evidence.

## Redis 8.2 wire semantics

Captured by:

```
compat/scripting/script-debug-ldb-wire.py
```

### Initial stop

The first `EVAL` after `SCRIPT DEBUG YES|SYNC` pauses before the first
executable source line.

Example:

```
* Stopped at 1, stop reason = step over
-> 1   local x = 10
```

### L — source listing

`L` returns a source window centered on the current line. The audited initial
stop returned lines 1-6; the later stop at line 9 returned lines 4-10.

The current line is marked with `->`. Breakpoint lines are marked with `#`.

### T — stack trace

At top level Redis returns:

```
In top level:
-> <line>   <source>
```

Nested-function stack formatting must be audited further once the VM hook is
available and SnugKV can pause inside nested frames.

### P — variable inspection

`P <name>` returns:

```
<value> 15
```

for visible locals. A local not yet in scope or an unknown name returns:

```
No such variable.
```

The initial stop at line 1 does not expose `x` before the assignment executes.

### S — step

`S` stops at the next Lua line event, including function-definition/control-flow
locations. In the audited script the first `S` from line 1 stopped at line 6.

### N — next

`N` performs step-over behavior. In the audited script it moved from line 6 to
the call site on line 7 without descending into the nested function.

### B — breakpoints

`B` with no arguments lists current breakpoints.

With none:

```
No breakpoints set. Use 'b <line>' to add one.
```

`B <line>` installs a breakpoint and returns a 3-line source window with the
breakpoint marked by `#`.

`B -<line>` removes it and returns:

```
Breakpoint removed.
```

When execution reaches an installed breakpoint Redis stops with:

```
* Stopped at <line>, stop reason = break point
->#<line>   <source>
```

### redis.debug(...)

`redis.debug()` does not itself pause execution. It appends debugger output to
the same reply stream:

```
<debug> line 4: "inside-add", 15
```

The value formatting must follow Redis LDB formatting rather than normal Lua
`tostring`.

### redis.breakpoint()

`redis.breakpoint()` causes a debugger stop after the call returns, at the next
source line:

```
* Stopped at 9, stop reason = redis.breakpoint() called
-> 9   local z = y * 2
```

An explicit line breakpoint on the `redis.breakpoint()` call fires first. A
subsequent continue then hits the runtime `redis.breakpoint()` stop.

### End of session

Completion returns the LDB end marker followed by the ordinary script result:

```
*1
+<endsession>
:30
```

### Invalid debugger commands

Unknown commands, malformed breakpoint arguments, and malformed print commands
return one LDB message:

```
<error> Unknown Redis Lua debugger command or wrong number of arguments.
```

An empty RESP bulk debugger command triggered Redis's protocol-error end-session
path in the wire audit.

## GopherLua feasibility

SnugKV currently uses:

```
github.com/yuin/gopher-lua v1.1.2
```

Upstream v1.1.2 already exposes the debugger data required by SnugKV:

- `LState.GetStack`
- `LState.GetInfo`
- `LState.GetLocal`
- `LState.SetLocal`
- `LState.GetUpvalue`
- `LState.SetUpvalue`
- `FunctionProto.DbgSourcePositions`
- `FunctionProto.DbgLocals`
- `FunctionProto.DbgUpvalues`

The missing capability is only an execution hook invoked as the VM crosses Lua
source lines.

A historical fork, `terminar/gopher-lua-with-debug-hook`, demonstrates the
minimal mechanism, but it is based on a 2019-era GopherLua tree and must NOT be
adopted wholesale.

## Required controlled fork delta

Create a SnugKV-controlled fork from upstream GopherLua v1.1.2 and add only:

1. line-hook callback state on `LState`;
2. optional call/return depth notifications if needed for exact `N` behavior;
3. hook invocation in both `mainLoop` and `mainLoopWithContext` before
   executing each bytecode instruction, emitting only when the Lua source line
   changes;
4. an exported Go API, preferably host-only rather than Lua `debug.sethook`,
   such as:

```go
type LineHook func(L *LState, event HookEvent)

type HookEvent struct {
    Line  int
    Depth int
}

func (L *LState) SetLineHook(LineHook)
```

SnugKV does not need to expose the Lua debug library.

The fork must retain all v1.1.2 tests and add focused tests proving that hooks:

- preserve ordinary execution when disabled;
- emit source-line changes in order;
- work in context-enabled execution;
- report nested-call depth correctly;
- do not recursively hook the hook callback itself;
- remain race-clean.

## SnugKV debugger architecture

SnugKV now uses a persistent `scriptDebugRuntime` that owns the paused-debug
session state, including source text, breakpoints, current stop metadata,
continue/step/next mode, queued `redis.debug()` messages, cancellation, and
the VM resume/event channels.

Debug execution runs in a dedicated goroutine. The GopherLua host line hook
publishes a stop event and blocks until the TCP debugger command resumes it.
`YES` executes against a cloned server/store; `SYNC` executes against the
real server while preserving the invoking ACL execution context.

A condition-variable/channel handshake should be used rather than busy waiting:

- VM hook publishes `debugStop` and waits;
- TCP debugger command updates mode/state and resumes VM;
- script completion publishes end-session result.

### Step

Resume until the next line event, regardless of depth.

### Next

Record the current frame depth and resume until a later line event whose depth is
less than or equal to the starting depth.

### Continue

Resume until:

- an installed breakpoint;
- `redis.breakpoint()`;
- script completion/error.

### P

At a paused hook, enumerate `GetLocal` on the current `Debug` frame. Initial
implementation should support Redis-compatible variable-name lookup. Expression
evaluation beyond a bare variable should be audited separately before claiming
support.

### T

Walk `GetStack(level)` + `GetInfo("Sln", ...)` until exhausted and render
Redis LDB stack lines.

### L / B

These operate entirely from the original source text and breakpoint set, but line
eligibility should be validated against compiled `DbgSourcePositions` so
breakpoints only target executable Lua lines.

### redis.debug

Add `debug` to the scripting Redis module only for active LDB execution. It
serializes arguments using Redis LDB formatting and appends them to the current
debug output buffer.

### redis.breakpoint

Add `breakpoint` only for active LDB execution. It marks a pending runtime
breakpoint. The next emitted Lua line hook stops with reason
`redis.breakpoint() called`.

## Safety requirements

- `SCRIPT DEBUG YES` must keep using the disposable logical store clone.
- `SCRIPT DEBUG SYNC` must keep the current ACL execution context.
- Existing five-second script execution limits must remain effective while the
  VM is running; time spent intentionally paused in the debugger must be handled
  separately so an idle debugger does not incorrectly time out mid-inspection.
- Connection close must cancel and release a paused debugger goroutine.
- `SCRIPT DEBUG NO` must clear pending debugger state safely.
- Debug execution must not leak Lua states or goroutines.
- Existing non-debug EVAL/EVALSHA behavior must remain byte-for-byte unchanged.

## Acceptance status

The audited implementation now satisfies the Redis 8.2 LDB wire oracle for the
covered surface. Redis and SnugKV produce identical debugger frames; the saved
harness outputs differ only in the printed target port.

Verified behavior includes:

1. initial pause, continue, step, and next;
2. YES rollback and SYNC persistence;
3. line breakpoints and runtime `redis.breakpoint()` interaction;
4. source listing, top-level stack trace, and local inspection;
5. `redis.debug()` message framing;
6. invalid debugger-command replies;
7. empty-command protocol-error end-session plus connection close;
8. focused race and vet checks.

Before merge, the branch must still pass the repository-wide race, vet, and RESP
fuzz gates.
