package server

import (
	"errors"
	"fmt"
	"strings"

	"snugkv/internal/engine"
)

type scriptDebugMode uint8

const (
	scriptDebugOff scriptDebugMode = iota
	scriptDebugAsync
	scriptDebugSync
)

type scriptDebugPending struct {
	command [][]byte
}

func (c *clientSession) setScriptDebugMode(mode scriptDebugMode) {
	c.mu.Lock()
	c.scriptDebugMode = mode
	if mode == scriptDebugOff {
		c.scriptDebugPending = nil
	}
	c.mu.Unlock()
}

func (c *clientSession) scriptDebugState() (scriptDebugMode, *scriptDebugPending) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var pending *scriptDebugPending
	if c.scriptDebugPending != nil {
		pending = &scriptDebugPending{
			command: cloneCommandArgs(c.scriptDebugPending.command),
		}
	}
	return c.scriptDebugMode, pending
}

func (c *clientSession) setScriptDebugPending(pending *scriptDebugPending) {
	c.mu.Lock()
	c.scriptDebugPending = pending
	c.mu.Unlock()
}

func scriptDebugStopReply(source string) []byte {
	meta, err := parseEvalScriptMetadata(source)
	if err == nil {
		source = meta.body
	}

	lineNo := 1
	if meta.flagged {
		lineNo = 2
	}
	line := source
	if newline := strings.IndexByte(source, '\n'); newline >= 0 {
		line = source[:newline]
	}
	line = strings.TrimSuffix(line, "\r")
	line = strings.NewReplacer("\r", " ", "\n", " ").Replace(line)

	return array(
		[]byte(fmt.Sprintf("+* Stopped at %d, stop reason = step over\r\n", lineNo)),
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

func (s *TCPServer) beginScriptDebugEval(
	session *clientSession,
	args [][]byte,
) (bool, []byte, error) {
	mode, pending := session.scriptDebugState()
	if mode == scriptDebugOff || pending != nil || len(args) == 0 {
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

	session.setScriptDebugPending(&scriptDebugPending{
		command: cloneCommandArgs(args),
	})
	return true, scriptDebugStopReply(source), nil
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

func (s *TCPServer) executeScriptDebugCommand(
	session *clientSession,
	auth *authSession,
	args [][]byte,
) (bool, []byte, error) {
	mode, pending := session.scriptDebugState()
	if pending == nil {
		return false, nil, nil
	}

	if len(args) != 1 {
		return true, nil, errors.New("ERR unknown debugger command")
	}

	command := strings.ToUpper(string(args[0]))
	if command != "C" && command != "CONTINUE" {
		return true, nil, errors.New("ERR unknown debugger command")
	}

	var (
		result []byte
		err    error
	)

	switch mode {
	case scriptDebugAsync:
		clone, cloneErr := cloneServerForScriptDebug(s.server)
		if cloneErr != nil {
			return true, nil, cloneErr
		}
		clone.durableMu.Lock()
		result, err = clone.withExecutionACLContextLocked(
			auth,
			pending.command,
			func() ([]byte, error) {
				return clone.executePressure(pending.command)
			},
		)
		clone.durableMu.Unlock()

	case scriptDebugSync:
		result, err = s.server.executeForSession(pending.command, auth)

	default:
		return true, nil, errors.New("ERR debugger is not enabled")
	}

	session.setScriptDebugPending(nil)
	if err != nil {
		return true, nil, err
	}
	return true, scriptDebugEndReply(result), nil
}
