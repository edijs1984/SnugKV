package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	lua "github.com/yuin/gopher-lua"
)

var readOnlyScriptingCommands = map[string]commandInfo{
	"EVAL_RO":    {3, 0, 0, 0, 0, false},
	"EVALSHA_RO": {3, 0, 0, 0, 0, false},
}

func init() {
	for name, info := range readOnlyScriptingCommands {
		commandTable[name] = info
	}
}

func isReadOnlyScriptEvalCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := readOnlyScriptingCommands[strings.ToUpper(string(args[0]))]
	return ok
}

type scriptExecutionCounter struct {
	mu    sync.Mutex
	depth int
}

var scriptExecutionCounters sync.Map // map[*Server]*scriptExecutionCounter

func scriptExecutionCounterForServer(s *Server) *scriptExecutionCounter {
	if current, ok := scriptExecutionCounters.Load(s); ok {
		return current.(*scriptExecutionCounter)
	}
	created := &scriptExecutionCounter{}
	actual, _ := scriptExecutionCounters.LoadOrStore(s, created)
	return actual.(*scriptExecutionCounter)
}

func enterScriptExecution(s *Server) func() {
	counter := scriptExecutionCounterForServer(s)
	counter.mu.Lock()
	counter.depth++
	counter.mu.Unlock()
	return func() {
		counter.mu.Lock()
		counter.depth--
		counter.mu.Unlock()
	}
}

func scriptExecutionActive(s *Server) bool {
	counter := scriptExecutionCounterForServer(s)
	counter.mu.Lock()
	defer counter.mu.Unlock()
	return counter.depth > 0
}

func (s *Server) executeReadOnlyScripting(args [][]byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("ERR empty command")
	}
	cmd := strings.ToUpper(string(args[0]))
	info, ok := readOnlyScriptingCommands[cmd]
	if !ok {
		return nil, fmt.Errorf("ERR unknown command '%s'", cmd)
	}
	if len(args) < info.min || info.max > 0 && len(args) > info.max {
		return nil, fmt.Errorf("ERR wrong number of arguments for '%s' command", strings.ToLower(cmd))
	}
	return s.executeEvalReadOnly(args, cmd == "EVALSHA_RO")
}

func (s *Server) executeEvalReadOnly(args [][]byte, bySHA bool) ([]byte, error) {
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
			if strings.HasPrefix(err.Error(), "ERR ") {
				return nil, err
			}
			return nil, fmt.Errorf("ERR Error compiling script (new function): %v", err)
		}
		sha = cache.put(source)
	}

	meta, err := parseEvalScriptMetadata(source)
	if err != nil {
		return nil, err
	}
	if err := s.rejectFlaggedScriptInvocationOOM(meta); err != nil {
		return nil, err
	}
	if err := validateReadOnlyEvalMetadata(meta); err != nil {
		return nil, err
	}

	return s.runLuaScriptReadOnly(meta.body, sha, keys, argv)
}

func (s *Server) runLuaScriptReadOnly(source, sha string, keys, argv [][]byte) ([]byte, error) {
	L := newScriptLuaState()
	defer L.Close()

	ctx, cancel := context.WithTimeout(context.Background(), scriptExecutionLimit)
	defer cancel()
	L.SetContext(ctx)

	L.SetGlobal("KEYS", luaBytesTable(L, keys))
	L.SetGlobal("ARGV", luaBytesTable(L, argv))
	L.SetGlobal("redis", s.luaRedisModuleReadOnly(L))

	fn, err := L.LoadString(source)
	if err != nil {
		return nil, fmt.Errorf("ERR Error compiling script (new function): %v", err)
	}
	L.Push(fn)
	if err := L.PCall(0, 1, nil); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errors.New("ERR Script timed out")
		}
		if sha == "" {
			sha = scriptSHA(source)
		}
		return nil, fmt.Errorf("ERR Error running script (call to f_%s): %v", sha, err)
	}

	result := L.Get(-1)
	return luaValueToRESP(result)
}

func (s *Server) luaRedisModuleReadOnly(L *lua.LState) *lua.LTable {
	module := L.NewTable()
	L.SetFuncs(module, map[string]lua.LGFunction{
		"call":         s.luaRedisCallReadOnly(false),
		"pcall":        s.luaRedisCallReadOnly(true),
		"error_reply":  luaRedisErrorReply,
		"status_reply": luaRedisStatusReply,
		"sha1hex":      luaRedisSHA1Hex,
	})
	return module
}

func scriptCommandWritesOrReplicates(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	cmd := strings.ToUpper(string(args[0]))
	if info, ok := commandTable[cmd]; ok && info.write {
		return true
	}
	// Redis treats PUBLISH/SPUBLISH as MAY_REPLICATE, so they are forbidden by
	// EVAL_RO/FCALL_RO even though they do not mutate the keyspace.
	//
	// PFCOUNT is also considered RW by Redis scripting semantics because it may
	// change the HyperLogLog's internal representation and propagate that change.
	switch cmd {
	case "PUBLISH", "SPUBLISH", "PFCOUNT":
		return true
	default:
		return false
	}
}

func (s *Server) luaRedisCallReadOnly(protected bool) lua.LGFunction {
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
		if scriptCommandWritesOrReplicates(args) {
			return luaPushCommandError(L, protected, errors.New("ERR Write commands are not allowed from read-only scripts."))
		}
		if err := s.authorizeExecutionNestedCommand(args); err != nil {
			return luaPushCommandError(L, protected, err)
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
		value, err := respToLuaValue(L, result)
		if err != nil {
			return luaPushCommandError(L, protected, fmt.Errorf("ERR internal script reply decode failed: %v", err))
		}
		L.Push(value)
		return 1
	}
}