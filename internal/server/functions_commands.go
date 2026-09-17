package server

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"

	lua "github.com/yuin/gopher-lua"
)

var functionCommands = map[string]commandInfo{
	"FUNCTION": {2, 0, 0, 0, 0, false},
	"FCALL":    {3, 0, 0, 0, 0, true},
	"FCALL_RO": {3, 0, 0, 0, 0, false},
}

func init() {
	for name, info := range functionCommands {
		commandTable[name] = info
	}
}

type registeredFunction struct {
	name           string
	description    string
	hasDescription bool
	flags          []string
	noWrites       bool
	callback       *lua.LFunction
	library        *functionLibrary
}

type functionLibrary struct {
	name      string
	code      string
	state     *lua.LState
	redis     *lua.LTable
	functions map[string]*registeredFunction
}

type functionRegistry struct {
	mu        sync.RWMutex
	libraries map[string]*functionLibrary
	functions map[string]*registeredFunction
}

var functionRegistries sync.Map // map[*Server]*functionRegistry

func functionRegistryForServer(s *Server) *functionRegistry {
	if current, ok := functionRegistries.Load(s); ok {
		return current.(*functionRegistry)
	}
	created := &functionRegistry{
		libraries: make(map[string]*functionLibrary),
		functions: make(map[string]*registeredFunction),
	}
	actual, _ := functionRegistries.LoadOrStore(s, created)
	return actual.(*functionRegistry)
}

func isFunctionCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := functionCommands[strings.ToUpper(string(args[0]))]
	return ok
}

func isFunctionCallCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	cmd := strings.ToUpper(string(args[0]))
	return cmd == "FCALL" || cmd == "FCALL_RO"
}

func isWritableFunctionCallCommand(args [][]byte) bool {
	return len(args) > 0 && strings.EqualFold(string(args[0]), "FCALL")
}

func (s *Server) executeFunctionCommand(args [][]byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("ERR empty command")
	}
	cmd := strings.ToUpper(string(args[0]))
	info, ok := functionCommands[cmd]
	if !ok {
		return nil, fmt.Errorf("ERR unknown command '%s'", cmd)
	}
	if len(args) < info.min || info.max > 0 && len(args) > info.max {
		return nil, fmt.Errorf("ERR wrong number of arguments for '%s' command", strings.ToLower(cmd))
	}

	switch cmd {
	case "FUNCTION":
		return s.executeFunctionAdmin(args)
	case "FCALL":
		return s.executeFCall(args, false)
	case "FCALL_RO":
		return s.executeFCall(args, true)
	default:
		return nil, fmt.Errorf("ERR unknown command '%s'", cmd)
	}
}

func validFunctionName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			continue
		}
		return false
	}
	return true
}

func parseFunctionLibraryMetadata(code string) (name, body string, err error) {
	line := code
	body = ""
	if newline := strings.IndexByte(code, '\n'); newline >= 0 {
		line = code[:newline]
		body = code[newline+1:]
	}
	if !strings.HasPrefix(line, "#!") {
		return "", "", errors.New("ERR Missing library metadata")
	}
	fields := strings.Fields(strings.TrimPrefix(line, "#!"))
	if len(fields) == 0 || !strings.EqualFold(fields[0], "lua") {
		return "", "", errors.New("ERR Engine not found")
	}
	for _, field := range fields[1:] {
		if strings.HasPrefix(field, "name=") {
			name = strings.TrimPrefix(field, "name=")
		}
	}
	if !validFunctionName(name) {
		return "", "", errors.New("ERR Library names can only contain letters, numbers, or underscores(_) and must be at least one character long")
	}
	return name, body, nil
}

func parseRegisteredFunction(L *lua.LState) (name string, callback *lua.LFunction, description string, hasDescription bool, flags []string, noWrites bool, err error) {
	if L.GetTop() == 2 {
		name = L.CheckString(1)
		fn, ok := L.Get(2).(*lua.LFunction)
		if !ok {
			return "", nil, "", false, nil, false, errors.New("callback must be a function")
		}
		callback = fn
	} else if L.GetTop() == 1 {
		definition, ok := L.Get(1).(*lua.LTable)
		if !ok {
			return "", nil, "", false, nil, false, errors.New("wrong number of arguments to redis.register_function")
		}
		nameValue := definition.RawGetString("function_name")
		nameString, ok := nameValue.(lua.LString)
		if !ok {
			return "", nil, "", false, nil, false, errors.New("function_name must be a string")
		}
		name = string(nameString)
		fn, ok := definition.RawGetString("callback").(*lua.LFunction)
		if !ok {
			return "", nil, "", false, nil, false, errors.New("callback must be a function")
		}
		callback = fn

		descriptionValue := definition.RawGetString("description")
		if descriptionValue != lua.LNil {
			descriptionString, ok := descriptionValue.(lua.LString)
			if !ok {
				return "", nil, "", false, nil, false, errors.New("description must be a string")
			}
			description = string(descriptionString)
			hasDescription = true
		}

		flagsValue := definition.RawGetString("flags")
		if flagsValue != lua.LNil {
			flagsTable, ok := flagsValue.(*lua.LTable)
			if !ok {
				return "", nil, "", false, nil, false, errors.New("flags must be a table")
			}
			for i := 1; i <= flagsTable.Len(); i++ {
				flagValue := flagsTable.RawGetInt(i)
				flagString, ok := flagValue.(lua.LString)
				if !ok {
					return "", nil, "", false, nil, false, errors.New("function flag must be a string")
				}
				flag := strings.ToLower(string(flagString))
				if flag != "no-writes" {
					return "", nil, "", false, nil, false, fmt.Errorf("unsupported function flag: %s", flag)
				}
				if !noWrites {
					flags = append(flags, flag)
				}
				noWrites = true
			}
		}
	} else {
		return "", nil, "", false, nil, false, errors.New("wrong number of arguments to redis.register_function")
	}

	if !validFunctionName(name) {
		return "", nil, "", false, nil, false, errors.New("Library names can only contain letters, numbers, or underscores(_) and must be at least one character long")
	}
	return name, callback, description, hasDescription, flags, noWrites, nil
}

func (s *Server) loadFunctionLibrary(code string) (*functionLibrary, error) {
	name, body, err := parseFunctionLibraryMetadata(code)
	if err != nil {
		return nil, err
	}

	L := newScriptLuaState()
	lib := &functionLibrary{
		name:      name,
		code:      code,
		state:     L,
		functions: make(map[string]*registeredFunction),
	}
	module := L.NewTable()
	module.RawSetString("error_reply", L.NewFunction(luaRedisErrorReply))
	module.RawSetString("status_reply", L.NewFunction(luaRedisStatusReply))
	module.RawSetString("sha1hex", L.NewFunction(luaRedisSHA1Hex))
	module.RawSetString("register_function", L.NewFunction(func(L *lua.LState) int {
		functionName, callback, description, hasDescription, flags, noWrites, parseErr := parseRegisteredFunction(L)
		if parseErr != nil {
			L.RaiseError("%s", parseErr.Error())
			return 0
		}
		lower := strings.ToLower(functionName)
		if _, exists := lib.functions[lower]; exists {
			L.RaiseError("Function already exists in the library")
			return 0
		}
		lib.functions[lower] = &registeredFunction{
			name:           functionName,
			description:    description,
			hasDescription: hasDescription,
			flags:          flags,
			noWrites:       noWrites,
			callback:       callback,
			library:        lib,
		}
		return 0
	}))
	lib.redis = module
	L.SetGlobal("redis", module)

	ctx, cancel := context.WithTimeout(context.Background(), scriptExecutionLimit)
	defer cancel()
	L.SetContext(ctx)
	if err := L.DoString(body); err != nil {
		L.Close()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errors.New("ERR Library load timed out")
		}
		return nil, fmt.Errorf("ERR Error registering functions: %v", err)
	}
	if len(lib.functions) == 0 {
		L.Close()
		return nil, errors.New("ERR No functions registered")
	}
	return lib, nil
}

func (r *functionRegistry) load(lib *functionLibrary, replace bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	old := r.libraries[lib.name]
	if old != nil && !replace {
		return fmt.Errorf("ERR Library '%s' already exists", lib.name)
	}
	for lower, fn := range lib.functions {
		if existing := r.functions[lower]; existing != nil && (old == nil || existing.library != old) {
			return fmt.Errorf("ERR Function %s already exists", fn.name)
		}
	}

	if old != nil {
		for lower := range old.functions {
			delete(r.functions, lower)
		}
	}
	r.libraries[lib.name] = lib
	for lower, fn := range lib.functions {
		r.functions[lower] = fn
	}
	if old != nil {
		old.state.Close()
	}
	return nil
}

func (r *functionRegistry) delete(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	lib := r.libraries[name]
	if lib == nil {
		return errors.New("ERR Library not found")
	}
	delete(r.libraries, name)
	for lower := range lib.functions {
		delete(r.functions, lower)
	}
	lib.state.Close()
	return nil
}

func (r *functionRegistry) flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, lib := range r.libraries {
		lib.state.Close()
	}
	r.libraries = make(map[string]*functionLibrary)
	r.functions = make(map[string]*registeredFunction)
}

func (r *functionRegistry) lookup(name string) *registeredFunction {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.functions[strings.ToLower(name)]
}

func functionGlobMatch(pattern, name string) bool {
	matched, err := path.Match(pattern, name)
	return err == nil && matched
}

func (s *Server) executeFunctionAdmin(args [][]byte) ([]byte, error) {
	subcommand := strings.ToUpper(string(args[1]))
	registry := functionRegistryForServer(s)

	switch subcommand {
	case "LOAD":
		replace := false
		var code string
		switch len(args) {
		case 3:
			code = string(args[2])
		case 4:
			if !strings.EqualFold(string(args[2]), "REPLACE") {
				return nil, errors.New("ERR syntax error")
			}
			replace = true
			code = string(args[3])
		default:
			return nil, errors.New("ERR wrong number of arguments for 'function|load' command")
		}
		lib, err := s.loadFunctionLibrary(code)
		if err != nil {
			return nil, err
		}
		if err := registry.load(lib, replace); err != nil {
			lib.state.Close()
			return nil, err
		}
		return formatBulkString([]byte(lib.name)), nil

	case "DELETE":
		if len(args) != 3 {
			return nil, errors.New("ERR wrong number of arguments for 'function|delete' command")
		}
		if err := registry.delete(string(args[2])); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil

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
		registry.flush()
		return []byte("+OK\r\n"), nil

	case "LIST":
		pattern := "*"
		withCode := false
		for i := 2; i < len(args); {
			switch strings.ToUpper(string(args[i])) {
			case "WITHCODE":
				if withCode {
					return nil, errors.New("ERR syntax error")
				}
				withCode = true
				i++
			case "LIBRARYNAME":
				if i+1 >= len(args) || pattern != "*" {
					return nil, errors.New("ERR syntax error")
				}
				pattern = string(args[i+1])
				i += 2
			default:
				return nil, errors.New("ERR syntax error")
			}
		}
		return registry.list(pattern, withCode), nil

	default:
		return nil, fmt.Errorf("ERR Unknown subcommand or wrong number of arguments for '%s'. Try FUNCTION HELP.", strings.ToLower(string(args[1])))
	}
}

func (r *functionRegistry) list(pattern string, withCode bool) []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.libraries))
	for name := range r.libraries {
		if functionGlobMatch(pattern, name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	libraries := make([][]byte, 0, len(names))
	for _, name := range names {
		lib := r.libraries[name]
		functionNames := make([]string, 0, len(lib.functions))
		for _, fn := range lib.functions {
			functionNames = append(functionNames, fn.name)
		}
		sort.Strings(functionNames)
		functionReplies := make([][]byte, 0, len(functionNames))
		for _, functionName := range functionNames {
			fn := lib.functions[strings.ToLower(functionName)]
			flagReplies := make([][]byte, 0, len(fn.flags))
			for _, flag := range fn.flags {
				flagReplies = append(flagReplies, formatBulkString([]byte(flag)))
			}
			description := nullBulk()
			if fn.hasDescription {
				description = formatBulkString([]byte(fn.description))
			}
			functionReplies = append(functionReplies, array(
				formatBulkString([]byte("name")), formatBulkString([]byte(fn.name)),
				formatBulkString([]byte("description")), description,
				formatBulkString([]byte("flags")), array(flagReplies...),
			))
		}
		items := [][]byte{
			formatBulkString([]byte("library_name")), formatBulkString([]byte(lib.name)),
			formatBulkString([]byte("engine")), formatBulkString([]byte("LUA")),
			formatBulkString([]byte("functions")), array(functionReplies...),
		}
		if withCode {
			items = append(items, formatBulkString([]byte("library_code")), formatBulkString([]byte(lib.code)))
		}
		libraries = append(libraries, array(items...))
	}
	return array(libraries...)
}

func parseFCallArguments(args [][]byte) (keys, argv [][]byte, err error) {
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

func (s *Server) executeFCall(args [][]byte, readOnly bool) ([]byte, error) {
	keys, argv, err := parseFCallArguments(args)
	if err != nil {
		return nil, err
	}
	fn := functionRegistryForServer(s).lookup(string(args[1]))
	if fn == nil {
		return nil, errors.New("ERR Function not found")
	}
	return s.runRegisteredFunction(fn, keys, argv, readOnly || fn.noWrites)
}

func (s *Server) runRegisteredFunction(fn *registeredFunction, keys, argv [][]byte, readOnly bool) ([]byte, error) {
	L := fn.library.state
	if readOnly {
		fn.library.redis.RawSetString("call", L.NewFunction(s.luaRedisCallReadOnly(false)))
		fn.library.redis.RawSetString("pcall", L.NewFunction(s.luaRedisCallReadOnly(true)))
	} else {
		fn.library.redis.RawSetString("call", L.NewFunction(s.luaRedisCall(false)))
		fn.library.redis.RawSetString("pcall", L.NewFunction(s.luaRedisCall(true)))
	}

	ctx, cancel := context.WithTimeout(context.Background(), scriptExecutionLimit)
	defer cancel()
	L.SetContext(ctx)
	if err := L.CallByParam(lua.P{Fn: fn.callback, NRet: 1, Protect: true}, luaBytesTable(L, keys), luaBytesTable(L, argv)); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errors.New("ERR Function timed out")
		}
		return nil, fmt.Errorf("ERR Error running function: %v", err)
	}
	result := L.Get(-1)
	L.Pop(1)
	return luaValueToRESP(result)
}
