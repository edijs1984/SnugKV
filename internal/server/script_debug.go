package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	lua "github.com/yuin/gopher-lua"
	"snugkv/internal/engine"
)

type scriptDebugMode uint8

const (
	scriptDebugOff scriptDebugMode = iota
	scriptDebugAsync
	scriptDebugSync
)

type scriptDebugResumeMode uint8

const (
	scriptDebugContinue scriptDebugResumeMode = iota
	scriptDebugStep
	scriptDebugNext
)

type scriptDebugResume struct {
	mode scriptDebugResumeMode
}

type scriptDebugEvent struct {
	line   int
	depth  int
	reason string
	result []byte
	err    error
	done   bool
}

type scriptDebugRuntime struct {
	command    [][]byte
	source     string
	body       string
	lineOffset int

	ctx    context.Context
	cancel context.CancelFunc

	resume chan scriptDebugResume
	events chan scriptDebugEvent

	closeOnce sync.Once
}

func newScriptDebugRuntime(command [][]byte, source string) (*scriptDebugRuntime, error) {
	meta, err := parseEvalScriptMetadata(source)
	if err != nil {
		return nil, err
	}

	offset := 0
	if meta.flagged {
		offset = 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &scriptDebugRuntime{
		command:    cloneCommandArgs(command),
		source:     source,
		body:       meta.body,
		lineOffset: offset,
		ctx:        ctx,
		cancel:     cancel,
		resume:     make(chan scriptDebugResume),
		events:     make(chan scriptDebugEvent, 1),
	}, nil
}

func (r *scriptDebugRuntime) close() {
	if r == nil {
		return
	}
	r.closeOnce.Do(r.cancel)
}

func (r *scriptDebugRuntime) displayLine(luaLine int) (int, string) {
	lineNo := luaLine + r.lineOffset
	lines := strings.Split(strings.ReplaceAll(r.body, "\r\n", "\n"), "\n")
	text := ""
	if luaLine > 0 && luaLine <= len(lines) {
		text = strings.TrimSuffix(lines[luaLine-1], "\r")
	}
	return lineNo, text
}

func (r *scriptDebugRuntime) lineHook() lua.LineHook {
	initialized := false
	mode := scriptDebugContinue
	nextDepth := 0

	pause := func(event lua.HookEvent, reason string) bool {
		select {
		case r.events <- scriptDebugEvent{
			line:   event.Line,
			depth:  event.Depth,
			reason: reason,
		}:
		case <-r.ctx.Done():
			return false
		}

		select {
		case resume := <-r.resume:
			mode = resume.mode
			if mode == scriptDebugNext {
				nextDepth = event.Depth
			}
			return true
		case <-r.ctx.Done():
			return false
		}
	}

	return func(_ *lua.LState, event lua.HookEvent) {
		if event.Line <= 0 || r.ctx.Err() != nil {
			return
		}

		if !initialized {
			initialized = true
			_ = pause(event, "step over")
			return
		}

		shouldStop := false
		switch mode {
		case scriptDebugStep:
			shouldStop = true
		case scriptDebugNext:
			shouldStop = event.Depth <= nextDepth
		case scriptDebugContinue:
			return
		}

		if shouldStop {
			_ = pause(event, "step over")
		}
	}
}

func (c *clientSession) setScriptDebugMode(mode scriptDebugMode) {
	c.mu.Lock()
	oldRuntime := c.scriptDebugRuntime
	c.scriptDebugMode = mode
	if mode == scriptDebugOff {
		c.scriptDebugRuntime = nil
	}
	c.mu.Unlock()

	if mode == scriptDebugOff && oldRuntime != nil {
		oldRuntime.close()
	}
}

func (c *clientSession) scriptDebugState() (scriptDebugMode, *scriptDebugRuntime) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.scriptDebugMode, c.scriptDebugRuntime
}

func (c *clientSession) setScriptDebugRuntime(runtime *scriptDebugRuntime) {
	c.mu.Lock()
	c.scriptDebugRuntime = runtime
	c.mu.Unlock()
}

func (c *clientSession) clearScriptDebugRuntime(runtime *scriptDebugRuntime) {
	c.mu.Lock()
	if c.scriptDebugRuntime == runtime {
		c.scriptDebugRuntime = nil
	}
	c.mu.Unlock()
}

func (c *clientSession) closeScriptDebugRuntime() {
	c.mu.Lock()
	runtime := c.scriptDebugRuntime
	c.scriptDebugRuntime = nil
	c.mu.Unlock()
	if runtime != nil {
		runtime.close()
	}
}

func scriptDebugStopReply(runtime *scriptDebugRuntime, event scriptDebugEvent) []byte {
	lineNo, line := runtime.displayLine(event.line)
	return array(
		[]byte(fmt.Sprintf("+* Stopped at %d, stop reason = %s\r\n", lineNo, event.reason)),
		[]byte(fmt.Sprintf("+-> %d   %s\r\n", lineNo, line)),
	)
}

func scriptDebugEndReply(result []byte) []byte {
	out := array([]byte("+<endsession>\r\n"))
	out = append(out, result...)
	return out
}

func (s *TCPServer) executeScriptDebugControl(
	session *clientSession,
	args [][]byte,
) (bool, []byte, error) {
	if len(args) < 2 ||
		!strings.EqualFold(string(args[0]), "SCRIPT") ||
		!strings.EqualFold(string(args[1]), "DEBUG") {
		return false, nil, nil
	}

	if len(args) != 3 {
		return true, nil, errors.New(
			"ERR wrong number of arguments for 'script|debug' command",
		)
	}

	switch strings.ToUpper(string(args[2])) {
	case "NO":
		session.setScriptDebugMode(scriptDebugOff)
	case "YES":
		session.setScriptDebugMode(scriptDebugAsync)
	case "SYNC":
		session.setScriptDebugMode(scriptDebugSync)
	default:
		return true, nil, errors.New("ERR Use SCRIPT DEBUG YES/SYNC/NO")
	}

	return true, []byte("+OK\r\n"), nil
}

func cloneServerForScriptDebug(source *Server) (*Server, error) {
	cloneStore := engine.New()
	if err := cloneStore.Restore(source.store.Export(nil), true); err != nil {
		return nil, err
	}
	cloneStore.SetMaxMemory(source.store.MaxMemory())

	clone := New(cloneStore)
	clone.eviction = source.eviction
	clone.acl = source.acl
	clone.aclLog = source.aclLog
	return clone, nil
}

func (s *TCPServer) runScriptDebugRuntime(
	runtime *scriptDebugRuntime,
	target *Server,
	auth *authSession,
) {
	target.durableMu.Lock()
	result, err := target.withExecutionACLContextLocked(
		auth,
		runtime.command,
		func() ([]byte, error) {
			return target.executeEvalDebug(runtime.command, runtime)
		},
	)
	target.durableMu.Unlock()

	event := scriptDebugEvent{result: result, err: err, done: true}
	select {
	case runtime.events <- event:
	case <-runtime.ctx.Done():
		select {
		case runtime.events <- event:
		default:
		}
	}
}

func (s *TCPServer) beginScriptDebugEval(
	session *clientSession,
	auth *authSession,
	args [][]byte,
) (bool, []byte, error) {
	mode, runtime := session.scriptDebugState()
	if mode == scriptDebugOff || runtime != nil || len(args) == 0 {
		return false, nil, nil
	}

	cmd := strings.ToUpper(string(args[0]))
	if cmd != "EVAL" {
		return false, nil, nil
	}
	if len(args) < 3 {
		return true, nil, errors.New("ERR wrong number of arguments for 'eval' command")
	}

	source := string(args[1])
	if err := validateLuaScript(source); err != nil {
		if strings.HasPrefix(err.Error(), "ERR ") {
			return true, nil, err
		}
		return true, nil, fmt.Errorf("ERR Error compiling script (new function): %v", err)
	}
	if _, _, err := parseEvalArguments(args); err != nil {
		return true, nil, err
	}

	runtime, err := newScriptDebugRuntime(args, source)
	if err != nil {
		return true, nil, err
	}

	target := s.server
	if mode == scriptDebugAsync {
		target, err = cloneServerForScriptDebug(s.server)
		if err != nil {
			runtime.close()
			return true, nil, err
		}
	}

	session.setScriptDebugRuntime(runtime)
	go s.runScriptDebugRuntime(runtime, target, auth)

	event := <-runtime.events
	if event.done {
		session.clearScriptDebugRuntime(runtime)
		runtime.close()
		if event.err != nil {
			return true, nil, event.err
		}
		return true, scriptDebugEndReply(event.result), nil
	}
	return true, scriptDebugStopReply(runtime, event), nil
}

func parseScriptDebugResume(args [][]byte) (scriptDebugResumeMode, bool) {
	if len(args) != 1 {
		return 0, false
	}
	switch strings.ToUpper(string(args[0])) {
	case "C", "CONTINUE":
		return scriptDebugContinue, true
	case "S", "STEP":
		return scriptDebugStep, true
	case "N", "NEXT":
		return scriptDebugNext, true
	default:
		return 0, false
	}
}

func (s *TCPServer) executeScriptDebugCommand(
	session *clientSession,
	auth *authSession,
	args [][]byte,
) (bool, []byte, error) {
	_, runtime := session.scriptDebugState()
	if runtime == nil {
		return false, nil, nil
	}

	mode, ok := parseScriptDebugResume(args)
	if !ok {
		return true, nil, errors.New("ERR unknown debugger command")
	}

	select {
	case runtime.resume <- scriptDebugResume{mode: mode}:
	case <-runtime.ctx.Done():
		session.clearScriptDebugRuntime(runtime)
		return true, nil, errors.New("ERR debugger session ended")
	}

	event := <-runtime.events
	if event.done {
		session.clearScriptDebugRuntime(runtime)
		runtime.close()
		if event.err != nil {
			return true, nil, event.err
		}
		return true, scriptDebugEndReply(event.result), nil
	}

	return true, scriptDebugStopReply(runtime, event), nil
}
