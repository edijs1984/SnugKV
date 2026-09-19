package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	lua "github.com/yuin/gopher-lua"
	"snugkv/internal/engine"
)

var errScriptDebugCloseAfterReply = errors.New("script debugger close connection after reply")

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
	line     int
	depth    int
	reason   string
	state    *lua.LState
	messages []string
	result   []byte
	err      error
	done     bool
}

type scriptDebugRuntime struct {
	command    [][]byte
	source     string
	body       string
	lineOffset int

	mu                       sync.RWMutex
	current                  scriptDebugEvent
	breakpoints              map[int]struct{}
	lastLine                 int
	pendingRuntimeBreakpoint bool
	debugMessages            []string

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
		breakpoints: make(map[int]struct{}),
	}, nil
}

func (r *scriptDebugRuntime) setCurrent(event scriptDebugEvent) {
	r.mu.Lock()
	r.current = event
	r.mu.Unlock()
}

func (r *scriptDebugRuntime) currentEvent() scriptDebugEvent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current
}

func (r *scriptDebugRuntime) clearCurrent() {
	r.mu.Lock()
	r.current = scriptDebugEvent{}
	r.mu.Unlock()
}

func (r *scriptDebugRuntime) hasBreakpoint(luaLine int) bool {
	r.mu.RLock()
	_, ok := r.breakpoints[luaLine]
	r.mu.RUnlock()
	return ok
}

func (r *scriptDebugRuntime) setBreakpoint(luaLine int, enabled bool) {
	r.mu.Lock()
	if enabled {
		r.breakpoints[luaLine] = struct{}{}
	} else {
		delete(r.breakpoints, luaLine)
	}
	r.mu.Unlock()
}

func (r *scriptDebugRuntime) breakpointLines() []int {
	r.mu.RLock()
	lines := make([]int, 0, len(r.breakpoints))
	for line := range r.breakpoints {
		lines = append(lines, line+r.lineOffset)
	}
	r.mu.RUnlock()
	sort.Ints(lines)
	return lines
}

func (r *scriptDebugRuntime) setLastLine(luaLine int) {
	r.mu.Lock()
	r.lastLine = luaLine
	r.mu.Unlock()
}

func (r *scriptDebugRuntime) addDebugMessage(values []lua.LValue) {
	r.mu.Lock()
	line := r.lastLine + r.lineOffset
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, scriptDebugValue(value))
	}
	r.debugMessages = append(
		r.debugMessages,
		fmt.Sprintf("<debug> line %d: %s", line, strings.Join(parts, ", ")),
	)
	r.mu.Unlock()
}

func (r *scriptDebugRuntime) requestRuntimeBreakpoint() {
	r.mu.Lock()
	r.pendingRuntimeBreakpoint = true
	r.mu.Unlock()
}

func (r *scriptDebugRuntime) takeRuntimeBreakpoint() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.pendingRuntimeBreakpoint {
		return false
	}
	r.pendingRuntimeBreakpoint = false
	return true
}

func (r *scriptDebugRuntime) drainDebugMessages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.debugMessages) == 0 {
		return nil
	}
	out := append([]string(nil), r.debugMessages...)
	r.debugMessages = r.debugMessages[:0]
	return out
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

	pause := func(L *lua.LState, event lua.HookEvent, reason string) bool {
		select {
		case r.events <- scriptDebugEvent{
			line:     event.Line,
			depth:    event.Depth,
			reason:   reason,
			state:    L,
			messages: r.drainDebugMessages(),
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

	return func(L *lua.LState, event lua.HookEvent) {
		if event.Line <= 0 || r.ctx.Err() != nil {
			return
		}
		r.setLastLine(event.Line)

		if !initialized {
			initialized = true
			_ = pause(L, event, "step over")
			return
		}

		shouldStop := false
		if r.takeRuntimeBreakpoint() {
			_ = pause(L, event, "redis.breakpoint() called")
			return
		}
		if r.hasBreakpoint(event.Line) {
			_ = pause(L, event, "break point")
			return
		}

		switch mode {
		case scriptDebugStep:
			if endLine, ok := r.localFunctionEnd(event.Line); ok {
				event.Line = endLine
			}
			shouldStop = true
		case scriptDebugNext:
			shouldStop = event.Depth <= nextDepth
		case scriptDebugContinue:
			return
		}

		if shouldStop {
			_ = pause(L, event, "step over")
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
	items := make([][]byte, 0, len(event.messages)+2)
	for _, message := range event.messages {
		items = append(items, []byte("+"+message+"\r\n"))
	}
	marker := "-> "
	if event.reason == "break point" && runtime.hasBreakpoint(event.line) {
		marker = "->#"
	}
	items = append(items,
		[]byte(fmt.Sprintf("+* Stopped at %d, stop reason = %s\r\n", lineNo, event.reason)),
		scriptDebugSourceLine(marker, lineNo, line),
	)
	return array(items...)
}

func scriptDebugEndReply(result []byte, messages ...string) []byte {
	items := make([][]byte, 0, len(messages)+1)
	for _, message := range messages {
		items = append(items, []byte("+"+message+"\r\n"))
	}
	items = append(items, []byte("+<endsession>\r\n"))
	out := array(items...)
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

	event := scriptDebugEvent{
		result:   result,
		err:      err,
		done:     true,
		messages: runtime.drainDebugMessages(),
	}
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
		return true, scriptDebugEndReply(event.result, event.messages...), nil
	}
	runtime.setCurrent(event)
	return true, scriptDebugStopReply(runtime, event), nil
}


func scriptDebugValue(value lua.LValue) string {
	switch v := value.(type) {
	case *lua.LNilType:
		return "nil"
	case lua.LBool:
		if bool(v) {
			return "true"
		}
		return "false"
	case lua.LNumber:
		return strconv.FormatFloat(float64(v), 'g', -1, 64)
	case lua.LString:
		return strconv.Quote(string(v))
	default:
		return value.String()
	}
}

func (r *scriptDebugRuntime) sourceLines() []string {
	return strings.Split(strings.ReplaceAll(r.body, "\r\n", "\n"), "\n")
}

func (r *scriptDebugRuntime) lineText(displayLine int) string {
	luaLine := displayLine - r.lineOffset
	lines := r.sourceLines()
	if luaLine < 1 || luaLine > len(lines) {
		return ""
	}
	return strings.TrimSuffix(lines[luaLine-1], "\r")
}

func (r *scriptDebugRuntime) localFunctionEnd(luaLine int) (int, bool) {
	lines := r.sourceLines()
	if luaLine < 1 || luaLine > len(lines) {
		return 0, false
	}
	start := strings.TrimSpace(lines[luaLine-1])
	if !strings.HasPrefix(start, "local function ") {
		return 0, false
	}

	depth := 1
	for i := luaLine; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "--") || line == "" {
			continue
		}

		if strings.HasPrefix(line, "function ") ||
			strings.HasPrefix(line, "local function ") ||
			strings.HasSuffix(line, " then") ||
			strings.HasPrefix(line, "for ") ||
			strings.HasPrefix(line, "while ") ||
			line == "repeat" ||
			line == "do" {
			depth++
		}

		if line == "end" || strings.HasPrefix(line, "end ") {
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
		if strings.HasPrefix(line, "until ") {
			depth--
		}
	}
	return 0, false
}

func scriptDebugSourceLine(prefix string, displayLine int, source string) []byte {
	number := strconv.Itoa(displayLine)
	padding := 4 - len(number)
	if padding < 1 {
		padding = 1
	}
	return []byte(fmt.Sprintf("+%s%s%s%s\r\n", prefix, number, strings.Repeat(" ", padding), source))
}

func scriptDebugErrorReply() []byte {
	return array([]byte("+<error> Unknown Redis Lua debugger command or wrong number of arguments.\r\n"))
}

func (r *scriptDebugRuntime) protocolErrorReply() []byte {
	sha := scriptSHA(r.source)
	return scriptDebugEndReply(
		[]byte(fmt.Sprintf("-ERR protocol error script: %s, on @user_script:1.\r\n", sha)),
	)
}


func (r *scriptDebugRuntime) listReply(centerDisplayLine, radius int) []byte {
	lines := r.sourceLines()
	center := centerDisplayLine - r.lineOffset
	if center < 1 {
		center = 1
	}
	start := center - radius
	if start < 1 {
		start = 1
	}
	end := center + radius
	if end > len(lines) {
		end = len(lines)
	}

	current := r.currentEvent().line
	items := make([][]byte, 0, end-start+1)
	for luaLine := start; luaLine <= end; luaLine++ {
		displayLine := luaLine + r.lineOffset
		source := strings.TrimSuffix(lines[luaLine-1], "\r")

		switch {
		case luaLine == current && r.hasBreakpoint(luaLine):
			items = append(items, scriptDebugSourceLine("->#", displayLine, source))
		case luaLine == current:
			items = append(items, scriptDebugSourceLine("-> ", displayLine, source))
		case r.hasBreakpoint(luaLine):
			items = append(items, scriptDebugSourceLine("  #", displayLine, source))
		default:
			items = append(items, scriptDebugSourceLine("   ", displayLine, source))
		}
	}
	return array(items...)
}

func (r *scriptDebugRuntime) printReply(name string) []byte {
	event := r.currentEvent()
	if event.state == nil {
		return array([]byte("+No such variable.\r\n"))
	}
	dbg, ok := event.state.GetStack(0)
	if !ok {
		return array([]byte("+No such variable.\r\n"))
	}
	for i := 1; ; i++ {
		localName, value := event.state.GetHookLocal(dbg, i)
		if localName == "" {
			break
		}
		if localName == name {
			return array([]byte(fmt.Sprintf("+<value> %s\r\n", scriptDebugValue(value))))
		}
	}
	return array([]byte("+No such variable.\r\n"))
}

func (r *scriptDebugRuntime) traceReply() []byte {
	event := r.currentEvent()
	if event.state == nil {
		return array([]byte("+In top level:\r\n"))
	}
	items := make([][]byte, 0, 4)
	for level := 0; ; level++ {
		dbg, ok := event.state.GetStack(level)
		if !ok {
			break
		}
		_, _ = event.state.GetInfo("Sln", dbg, lua.LNil)
		if level == 0 && dbg.What == "main" {
			items = append(items, []byte("+In top level:\r\n"))
		} else {
			name := dbg.Name
			if name == "" {
				name = "function"
			}
			items = append(items, []byte(fmt.Sprintf("+In %s:\r\n", name)))
		}
		line := dbg.CurrentLine
		if line <= 0 && level == 0 {
			line = event.line
		}
		display, source := r.displayLine(line)
		items = append(items, []byte(fmt.Sprintf("+-> %d   %s\r\n", display, source)))
	}
	if len(items) == 0 {
		items = append(items, []byte("+In top level:\r\n"))
	}
	return array(items...)
}

func isScriptDebugIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if i == 0 {
			if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
				return false
			}
			continue
		}
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func scriptDebugCommandParts(args [][]byte) []string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		fields := strings.Fields(string(arg))
		parts = append(parts, fields...)
	}
	return parts
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

	if len(args) == 1 && len(args[0]) == 0 {
		session.clearScriptDebugRuntime(runtime)
		runtime.close()
		return true, runtime.protocolErrorReply(), errScriptDebugCloseAfterReply
	}

	parts := scriptDebugCommandParts(args)
	if len(parts) == 0 {
		return true, scriptDebugErrorReply(), nil
	}

	command := strings.ToUpper(parts[0])
	switch command {
	case "L":
		if len(parts) != 1 {
			return true, scriptDebugErrorReply(), nil
		}
		event := runtime.currentEvent()
		display, _ := runtime.displayLine(event.line)
		return true, runtime.listReply(display, 5), nil

	case "P":
		if len(parts) != 2 || !isScriptDebugIdentifier(parts[1]) {
			return true, scriptDebugErrorReply(), nil
		}
		return true, runtime.printReply(parts[1]), nil

	case "T":
		if len(parts) != 1 {
			return true, scriptDebugErrorReply(), nil
		}
		return true, runtime.traceReply(), nil

	case "B":
		if len(parts) == 1 {
			lines := runtime.breakpointLines()
			if len(lines) == 0 {
				return true, array([]byte("+No breakpoints set. Use 'b <line>' to add one.\r\n")), nil
			}
			items := make([][]byte, 0, len(lines)+1)
			items = append(items, []byte(fmt.Sprintf("+%d breakpoints set:\r\n", len(lines))))
			for _, line := range lines {
				items = append(items, scriptDebugSourceLine("  #", line, runtime.lineText(line)))
			}
			return true, array(items...), nil
		}
		if len(parts) != 2 {
			return true, scriptDebugErrorReply(), nil
		}

		raw := parts[1]
		remove := strings.HasPrefix(raw, "-")
		if remove {
			raw = strings.TrimPrefix(raw, "-")
		}
		displayLine, err := strconv.Atoi(raw)
		if err != nil {
			return true, scriptDebugErrorReply(), nil
		}
		luaLine := displayLine - runtime.lineOffset
		lines := runtime.sourceLines()
		if luaLine < 1 || luaLine > len(lines) {
			return true, scriptDebugErrorReply(), nil
		}
		if remove {
			runtime.setBreakpoint(luaLine, false)
			return true, array([]byte("+Breakpoint removed.\r\n")), nil
		}
		runtime.setBreakpoint(luaLine, true)
		return true, runtime.listReply(displayLine, 1), nil
	}

	mode, ok := parseScriptDebugResume(args)
	if !ok {
		return true, scriptDebugErrorReply(), nil
	}

	runtime.clearCurrent()
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
		return true, scriptDebugEndReply(event.result, event.messages...), nil
	}

	runtime.setCurrent(event)
	return true, scriptDebugStopReply(runtime, event), nil
}
