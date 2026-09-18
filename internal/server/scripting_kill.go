package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	lua "github.com/yuin/gopher-lua"
)

var errScriptKilled = errors.New("ERR Script killed by user with SCRIPT KILL...")

type runningScript struct {
	ctx        context.Context
	cancel     context.CancelFunc
	writeDirty bool
	killed     bool
}

type runningScriptState struct {
	mu     sync.RWMutex
	active *runningScript
}

var runningScriptStates sync.Map // map[*Server]*runningScriptState

func runningScriptStateForServer(s *Server) *runningScriptState {
	if current, ok := runningScriptStates.Load(s); ok {
		return current.(*runningScriptState)
	}
	created := &runningScriptState{}
	actual, _ := runningScriptStates.LoadOrStore(s, created)
	return actual.(*runningScriptState)
}

func isScriptKillCommand(args [][]byte) bool {
	return len(args) >= 2 && strings.EqualFold(string(args[0]), "SCRIPT") && strings.EqualFold(string(args[1]), "KILL")
}

func beginRunningScript(s *Server) func() {
	ctx, cancel := context.WithCancel(context.Background())
	entry := &runningScript{ctx: ctx, cancel: cancel}
	state := runningScriptStateForServer(s)
	state.mu.Lock()
	state.active = entry
	state.mu.Unlock()
	return func() {
		state.mu.Lock()
		if state.active == entry {
			state.active = nil
		}
		state.mu.Unlock()
		cancel()
	}
}

func runningScriptContext(s *Server) context.Context {
	state := runningScriptStateForServer(s)
	state.mu.RLock()
	defer state.mu.RUnlock()
	if state.active == nil {
		return context.Background()
	}
	return state.active.ctx
}

func runningScriptWasKilled(s *Server) bool {
	state := runningScriptStateForServer(s)
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.active != nil && state.active.killed
}

// markRunningScriptWrite serializes the first write boundary with SCRIPT KILL.
// Either KILL wins first and the write is rejected, or the write wins first and
// later KILL attempts become UNKILLABLE.
func markRunningScriptWrite(s *Server) error {
	state := runningScriptStateForServer(s)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.active == nil {
		return nil
	}
	if state.active.killed {
		return errScriptKilled
	}
	state.active.writeDirty = true
	return nil
}

func (s *Server) executeKillableScripting(args [][]byte) ([]byte, error) {
	if len(args) < 3 {
		cmd := "eval"
		if len(args) > 0 {
			cmd = strings.ToLower(string(args[0]))
		}
		return nil, fmt.Errorf("ERR wrong number of arguments for '%s' command", cmd)
	}
	cmd := strings.ToUpper(string(args[0]))
	readOnly := cmd == "EVAL_RO" || cmd == "EVALSHA_RO"
	bySHA := cmd == "EVALSHA" || cmd == "EVALSHA_RO"
	if cmd != "EVAL" && cmd != "EVALSHA" && cmd != "EVAL_RO" && cmd != "EVALSHA_RO" {
		return nil, fmt.Errorf("ERR unknown command '%s'", cmd)
	}

	keys, argv, err := parseEvalArguments(args)
	if err != nil {
		return nil, err
	}
	cache := scriptCacheForServer(s)
	source := string(args[1])
	sha := ""
	if bySHA {
		sha = strings.ToLower(source)
		var ok bool
		source, ok = cache.get(sha)
		if !ok {
			return nil, errors.New("NOSCRIPT No matching script. Please use EVAL.")
		}
	} else {
		if err := validateLuaScript(source); err != nil {
			return nil, fmt.Errorf("ERR Error compiling script (new function): %v", err)
		}
		sha = cache.put(source)
	}

	finish := beginRunningScript(s)
	defer finish()
	return s.runKillableLuaScript(source, sha, keys, argv, readOnly)
}

func (s *Server) runKillableLuaScript(source, sha string, keys, argv [][]byte, readOnly bool) ([]byte, error) {
	L := newScriptLuaState()
	defer L.Close()

	ctx, cancel := context.WithTimeout(runningScriptContext(s), scriptExecutionLimit)
	defer cancel()
	L.SetContext(ctx)
	L.SetGlobal("KEYS", luaBytesTable(L, keys))
	L.SetGlobal("ARGV", luaBytesTable(L, argv))
	if readOnly {
		L.SetGlobal("redis", s.luaRedisModuleReadOnly(L))
	} else {
		L.SetGlobal("redis", s.luaRedisModuleKillable(L))
	}

	fn, err := L.LoadString(source)
	if err != nil {
		return nil, fmt.Errorf("ERR Error compiling script (new function): %v", err)
	}
	L.Push(fn)
	if err := L.PCall(0, 1, nil); err != nil {
		if runningScriptWasKilled(s) || errors.Is(ctx.Err(), context.Canceled) {
			return nil, errScriptKilled
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errors.New("ERR Script timed out")
		}
		if sha == "" {
			sha = scriptSHA(source)
		}
		return nil, fmt.Errorf("ERR Error running script (call to f_%s): %v", sha, err)
	}
	if runningScriptWasKilled(s) {
		return nil, errScriptKilled
	}
	result := L.Get(-1)
	return luaValueToRESP(result)
}

func (s *Server) luaRedisModuleKillable(L *lua.LState) *lua.LTable {
	module := L.NewTable()
	L.SetFuncs(module, map[string]lua.LGFunction{
		"call":         s.luaRedisCallKillable(false),
		"pcall":        s.luaRedisCallKillable(true),
		"error_reply":  luaRedisErrorReply,
		"status_reply": luaRedisStatusReply,
		"sha1hex":      luaRedisSHA1Hex,
	})
	return module
}

func (s *Server) luaRedisCallKillable(protected bool) lua.LGFunction {
	return func(L *lua.LState) int {
		if L.GetTop() < 1 {
			return luaPushCommandError(L, protected, errors.New("ERR Please specify at least one argument for redis.call()"))
		}
		args := make([][]byte, 0, L.GetTop())
		for i := 1; i <= L.GetTop(); i++ {
			arg, err := luaCommandArg(L.Get(i))
			if err != nil {
				return luaPushCommandError(L, protected, fmt.Errorf("ERR %v", err))
			}
			args = append(args, arg)
		}
		if scriptCommandForbidden(args) || isReadOnlyScriptEvalCommand(args) {
			return luaPushCommandError(L, protected, errors.New("ERR This Redis command is not allowed from script"))
		}
		if err := queuedCommandValidation(args); err != nil {
			if strings.Contains(err.Error(), "wrong number of arguments") {
				err = errors.New("ERR Wrong number of args calling Redis command from script")
			}
			return luaPushCommandError(L, protected, err)
		}
		if err := s.authorizeExecutionNestedCommand(args); err != nil {
			return luaPushCommandError(L, protected, err)
		}
		if scriptCommandWritesDataset(args) {
			if err := markRunningScriptWrite(s); err != nil {
				return luaPushCommandError(L, protected, err)
			}
		}

		result, err := s.withNestedExecutionCommand(
			args,
			func() ([]byte, error) {
				return s.executePressureMode(args, false)
			},
		)
		if err != nil {
			return luaPushCommandError(L, protected, err)
		}
		s.signalListAvailability(args, result)
		s.signalZSetAvailability(args, result)
		s.signalStreamAvailability(args, result)
		s.refreshWatchesLocked()

		value, err := respToLuaValue(L, result)
		if err != nil {
			return luaPushCommandError(L, protected, fmt.Errorf("ERR internal script reply decode failed: %v", err))
		}
		L.Push(value)
		return 1
	}
}

func (s *Server) executeScriptKill(args [][]byte) ([]byte, error) {
	if len(args) != 2 {
		return nil, errors.New("ERR wrong number of arguments for 'script|kill' command")
	}
	state := runningScriptStateForServer(s)
	state.mu.Lock()
	active := state.active
	if active == nil {
		state.mu.Unlock()
		return nil, errors.New("NOTBUSY No scripts in execution right now.")
	}
	if active.writeDirty {
		state.mu.Unlock()
		return nil, errors.New("UNKILLABLE Sorry the script already executed write commands against the dataset. You can either wait the script termination or kill the server in a hard way using the SHUTDOWN NOSAVE command.")
	}
	active.killed = true
	cancel := active.cancel
	state.mu.Unlock()
	cancel()
	return []byte("+OK\r\n"), nil
}

func (s *Server) executeScriptIntrospection(args [][]byte) ([]byte, bool, error) {
	if !isScriptKillCommand(args) {
		return nil, false, nil
	}
	response, err := s.executeScriptKill(args)
	return response, true, err
}