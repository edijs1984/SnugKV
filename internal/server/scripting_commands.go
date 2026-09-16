package server

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	lua "github.com/yuin/gopher-lua"
)

const scriptExecutionLimit = 5 * time.Second

var scriptingCommands = map[string]commandInfo{
	"EVAL":    {3, 0, 0, 0, 0, true},
	"EVALSHA": {3, 0, 0, 0, 0, true},
	"SCRIPT":  {2, 0, 0, 0, 0, false},
}

func init() {
	for name, info := range scriptingCommands {
		commandTable[name] = info
	}
}

type scriptCache struct {
	mu      sync.RWMutex
	scripts map[string]string
}

var scriptCaches sync.Map // map[*Server]*scriptCache

func scriptCacheForServer(s *Server) *scriptCache {
	if current, ok := scriptCaches.Load(s); ok {
		return current.(*scriptCache)
	}
	created := &scriptCache{scripts: make(map[string]string)}
	actual, _ := scriptCaches.LoadOrStore(s, created)
	return actual.(*scriptCache)
}

func scriptSHA(source string) string {
	sum := sha1.Sum([]byte(source))
	return hex.EncodeToString(sum[:])
}

func (c *scriptCache) put(source string) string {
	sha := scriptSHA(source)
	c.mu.Lock()
	c.scripts[sha] = source
	c.mu.Unlock()
	return sha
}

func (c *scriptCache) get(sha string) (string, bool) {
	c.mu.RLock()
	source, ok := c.scripts[strings.ToLower(sha)]
	c.mu.RUnlock()
	return source, ok
}

func (c *scriptCache) exists(sha string) bool {
	_, ok := c.get(sha)
	return ok
}

func (c *scriptCache) flush() {
	c.mu.Lock()
	c.scripts = make(map[string]string)
	c.mu.Unlock()
}

func isScriptingCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := scriptingCommands[strings.ToUpper(string(args[0]))]
	return ok
}

func isScriptEvalCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	cmd := strings.ToUpper(string(args[0]))
	return cmd == "EVAL" || cmd == "EVALSHA"
}

func (s *Server) executeScripting(args [][]byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("ERR empty command")
	}
	cmd := strings.ToUpper(string(args[0]))
	info, ok := scriptingCommands[cmd]
	if !ok {
		return nil, fmt.Errorf("ERR unknown command '%s'", cmd)
	}
	if len(args) < info.min || info.max > 0 && len(args) > info.max {
		return nil, fmt.Errorf("ERR wrong number of arguments for '%s' command", strings.ToLower(cmd))
	}

	switch cmd {
	case "SCRIPT":
		return s.executeScriptCommand(args)
	case "EVAL":
		return s.executeEval(args, false)
	case "EVALSHA":
		return s.executeEval(args, true)
	default:
		return nil, fmt.Errorf("ERR unknown command '%s'", cmd)
	}
}

func (s *Server) executeScriptCommand(args [][]byte) ([]byte, error) {
	subcommand := strings.ToUpper(string(args[1]))
	cache := scriptCacheForServer(s)

	switch subcommand {
	case "LOAD":
		if len(args) != 3 {
			return nil, errors.New("ERR wrong number of arguments for 'script|load' command")
		}
		source := string(args[2])
		if err := validateLuaScript(source); err != nil {
			return nil, fmt.Errorf("ERR Error compiling script (new function): %v", err)
		}
		sha := cache.put(source)
		return formatBulkString([]byte(sha)), nil

	case "EXISTS":
		if len(args) < 3 {
			return nil, errors.New("ERR wrong number of arguments for 'script|exists' command")
		}
		results := make([][]byte, 0, len(args)-2)
		for _, rawSHA := range args[2:] {
			results = append(results, boolean(cache.exists(string(rawSHA))))
		}
		return array(results...), nil

	case "FLUSH":
		if len(args) > 3 {
			return nil, errors.New("ERR syntax error")
		}
		if len(args) == 3 {
			mode := strings.ToUpper(string(args[2]))
			if mode != "SYNC" && mode != "ASYNC" {
				return nil, errors.New("ERR syntax error")
			}
		}
		cache.flush()
		return []byte("+OK\r\n"), nil

	default:
		return nil, fmt.Errorf("ERR Unknown subcommand or wrong number of arguments for '%s'. Try SCRIPT HELP.", strings.ToLower(string(args[1])))
	}
}

func validateLuaScript(source string) error {
	L := newScriptLuaState()
	defer L.Close()
	_, err := L.LoadString(source)
	return err
}

func parseEvalArguments(args [][]byte) (keys, argv [][]byte, err error) {
	numKeys, parseErr := strconv.ParseInt(string(args[2]), 10, 64)
	if parseErr != nil {
		return nil, nil, errors.New("ERR value is not an integer or out of range")
	}
	if numKeys < 0 {
		return nil, nil, errors.New("ERR Number of keys can't be negative")
	}
	if numKeys > int64(len(args)-3) {
		return nil, nil, errors.New("ERR Number of keys can't be greater than number of args")
	}
	keyEnd := 3 + int(numKeys)
	return args[3:keyEnd], args[keyEnd:], nil
}

func (s *Server) executeEval(args [][]byte, bySHA bool) ([]byte, error) {
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
		sha = scriptSHA(source)
	}

	response, err := s.runLuaScript(source, sha, keys, argv)
	if err != nil {
		return nil, err
	}
	if !bySHA {
		cache.put(source)
	}
	return response, nil
}

func newScriptLuaState() *lua.LState {
	L := lua.NewState(lua.Options{SkipOpenLibs: true})
	for _, lib := range []struct {
		name string
		open lua.LGFunction
	}{
		{lua.BaseLibName, lua.OpenBase},
		{lua.TabLibName, lua.OpenTable},
		{lua.StringLibName, lua.OpenString},
		{lua.MathLibName, lua.OpenMath},
	} {
		_ = L.CallByParam(lua.P{Fn: L.NewFunction(lib.open), NRet: 0, Protect: true}, lua.LString(lib.name))
	}
	// Redis scripts do not have filesystem/process access. OpenBase exposes file
	// loading helpers, so remove them explicitly while retaining ordinary Lua 5.1
	// language helpers such as tonumber, tostring, pcall, and loadstring.
	L.SetGlobal("dofile", lua.LNil)
	L.SetGlobal("loadfile", lua.LNil)
	L.SetGlobal("require", lua.LNil)
	L.SetGlobal("package", lua.LNil)
	L.SetGlobal("io", lua.LNil)
	L.SetGlobal("os", lua.LNil)
	return L
}

func luaBytesTable(L *lua.LState, values [][]byte) *lua.LTable {
	table := L.NewTable()
	for i, value := range values {
		table.RawSetInt(i+1, lua.LString(string(value)))
	}
	return table
}

func (s *Server) runLuaScript(source, sha string, keys, argv [][]byte) ([]byte, error) {
	L := newScriptLuaState()
	defer L.Close()

	ctx, cancel := context.WithTimeout(context.Background(), scriptExecutionLimit)
	defer cancel()
	L.SetContext(ctx)

	L.SetGlobal("KEYS", luaBytesTable(L, keys))
	L.SetGlobal("ARGV", luaBytesTable(L, argv))
	L.SetGlobal("redis", s.luaRedisModule(L))

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

func (s *Server) luaRedisModule(L *lua.LState) *lua.LTable {
	module := L.NewTable()
	L.SetFuncs(module, map[string]lua.LGFunction{
		"call":        s.luaRedisCall(false),
		"pcall":       s.luaRedisCall(true),
		"error_reply": luaRedisErrorReply,
		"status_reply": luaRedisStatusReply,
		"sha1hex":     luaRedisSHA1Hex,
	})
	return module
}

func luaCommandArg(value lua.LValue) ([]byte, error) {
	switch v := value.(type) {
	case lua.LString:
		return []byte(string(v)), nil
	case lua.LNumber:
		return []byte(strconv.FormatFloat(float64(v), 'g', -1, 64)), nil
	default:
		return nil, fmt.Errorf("Lua redis() command arguments must be strings or integers")
	}
}

func scriptCommandForbidden(args [][]byte) bool {
	if len(args) == 0 {
		return true
	}
	if isBlockingListCommand(args) || isBlockingZSetCommand(args) || isBlockingStreamCommand(args) {
		return true
	}
	cmd := strings.ToUpper(string(args[0]))
	if strings.HasPrefix(cmd, "SNUG.") {
		return true
	}
	switch cmd {
	case "EVAL", "EVALSHA", "SCRIPT",
		"MULTI", "EXEC", "DISCARD", "WATCH", "UNWATCH",
		"SUBSCRIBE", "UNSUBSCRIBE", "PSUBSCRIBE", "PUNSUBSCRIBE", "SSUBSCRIBE", "SUNSUBSCRIBE", "RESET",
		"QUIT", "SELECT", "HELLO", "COMMAND", "INFO", "MEMORY":
		return true
	default:
		return false
	}
}

func (s *Server) luaRedisCall(protected bool) lua.LGFunction {
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
		if scriptCommandForbidden(args) {
			return luaPushCommandError(L, protected, errors.New("ERR This Redis command is not allowed from script"))
		}

		result, err := s.executePressureMode(args, false)
		if err != nil {
			return luaPushCommandError(L, protected, err)
		}
		s.signalListAvailability(args, result)
		s.signalZSetAvailability(args, result)
		s.signalStreamAvailability(args, result)
		// WATCH must notice transient mutations made by a script even when a
		// later redis.call restores the original value before EVAL returns.
		s.refreshWatchesLocked()

		value, err := respToLuaValue(L, result)
		if err != nil {
			return luaPushCommandError(L, protected, fmt.Errorf("ERR internal script reply decode failed: %v", err))
		}
		L.Push(value)
		return 1
	}
}

func luaPushCommandError(L *lua.LState, protected bool, err error) int {
	if protected {
		table := L.NewTable()
		table.RawSetString("err", lua.LString(err.Error()))
		L.Push(table)
		return 1
	}
	L.RaiseError("%s", err.Error())
	return 0
}

func luaRedisErrorReply(L *lua.LState) int {
	message := L.CheckString(1)
	table := L.NewTable()
	table.RawSetString("err", lua.LString(message))
	L.Push(table)
	return 1
}

func luaRedisStatusReply(L *lua.LState) int {
	message := L.CheckString(1)
	table := L.NewTable()
	table.RawSetString("ok", lua.LString(message))
	L.Push(table)
	return 1
}

func luaRedisSHA1Hex(L *lua.LState) int {
	value := L.CheckString(1)
	L.Push(lua.LString(scriptSHA(value)))
	return 1
}

type respScriptDecoder struct {
	data []byte
	pos  int
}

func respToLuaValue(L *lua.LState, response []byte) (lua.LValue, error) {
	decoder := &respScriptDecoder{data: response}
	value, err := decoder.value(L)
	if err != nil {
		return lua.LNil, err
	}
	if decoder.pos != len(response) {
		return lua.LNil, errors.New("trailing RESP data")
	}
	return value, nil
}

func (d *respScriptDecoder) line() ([]byte, error) {
	start := d.pos
	for d.pos+1 < len(d.data) {
		if d.data[d.pos] == '\r' && d.data[d.pos+1] == '\n' {
			line := d.data[start:d.pos]
			d.pos += 2
			return line, nil
		}
		d.pos++
	}
	return nil, errors.New("unterminated RESP line")
}

func (d *respScriptDecoder) value(L *lua.LState) (lua.LValue, error) {
	if d.pos >= len(d.data) {
		return lua.LNil, errors.New("empty RESP reply")
	}
	kind := d.data[d.pos]
	d.pos++
	line, err := d.line()
	if err != nil {
		return lua.LNil, err
	}

	switch kind {
	case '+':
		table := L.NewTable()
		table.RawSetString("ok", lua.LString(string(line)))
		return table, nil
	case '-':
		table := L.NewTable()
		table.RawSetString("err", lua.LString(string(line)))
		return table, nil
	case ':':
		n, err := strconv.ParseInt(string(line), 10, 64)
		if err != nil {
			return lua.LNil, err
		}
		return lua.LNumber(n), nil
	case '$':
		n, err := strconv.ParseInt(string(line), 10, 64)
		if err != nil {
			return lua.LNil, err
		}
		if n == -1 {
			return lua.LFalse, nil
		}
		if n < 0 || n > int64(len(d.data)-d.pos-2) {
			return lua.LNil, errors.New("invalid RESP bulk length")
		}
		end := d.pos + int(n)
		value := lua.LString(string(d.data[d.pos:end]))
		d.pos = end
		if d.pos+2 > len(d.data) || d.data[d.pos] != '\r' || d.data[d.pos+1] != '\n' {
			return lua.LNil, errors.New("invalid RESP bulk terminator")
		}
		d.pos += 2
		return value, nil
	case '*':
		n, err := strconv.ParseInt(string(line), 10, 64)
		if err != nil {
			return lua.LNil, err
		}
		if n == -1 {
			return lua.LFalse, nil
		}
		if n < 0 {
			return lua.LNil, errors.New("invalid RESP array length")
		}
		table := L.NewTable()
		for i := int64(1); i <= n; i++ {
			item, err := d.value(L)
			if err != nil {
				return lua.LNil, err
			}
			table.RawSetInt(int(i), item)
		}
		return table, nil
	default:
		return lua.LNil, fmt.Errorf("unsupported RESP type %q", kind)
	}
}

func luaValueToRESP(value lua.LValue) ([]byte, error) {
	if value == lua.LNil || value == lua.LFalse {
		return nullBulk(), nil
	}
	if value == lua.LTrue {
		return integer(1), nil
	}

	switch v := value.(type) {
	case lua.LNumber:
		return integer(int64(float64(v))), nil
	case lua.LString:
		return formatBulkString([]byte(string(v))), nil
	case *lua.LTable:
		if errValue := v.RawGetString("err"); errValue != lua.LNil {
			message, ok := errValue.(lua.LString)
			if !ok {
				return nil, errors.New("ERR invalid error reply from script")
			}
			return nil, errors.New(string(message))
		}
		if okValue := v.RawGetString("ok"); okValue != lua.LNil {
			message, ok := okValue.(lua.LString)
			if !ok {
				return nil, errors.New("ERR invalid status reply from script")
			}
			text := strings.NewReplacer("\r", " ", "\n", " ").Replace(string(message))
			return []byte("+" + text + "\r\n"), nil
		}
		items := make([][]byte, 0)
		for i := 1; ; i++ {
			item := v.RawGetInt(i)
			if item == lua.LNil {
				break
			}
			encoded, err := luaValueToRESP(item)
			if err != nil {
				return nil, err
			}
			items = append(items, encoded)
		}
		return array(items...), nil
	default:
		return nil, fmt.Errorf("ERR Lua script returned unsupported type %s", value.Type().String())
	}
}
