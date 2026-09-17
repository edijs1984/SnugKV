package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

type runningFunction struct {
	name       string
	command    [][]byte
	started    time.Time
	ctx        context.Context
	cancel     context.CancelFunc
	writeDirty bool
	killed     bool
}

type runningFunctionState struct {
	mu     sync.RWMutex
	active *runningFunction
}

var runningFunctionStates sync.Map // map[*Server]*runningFunctionState

func runningFunctionStateForServer(s *Server) *runningFunctionState {
	if current, ok := runningFunctionStates.Load(s); ok {
		return current.(*runningFunctionState)
	}
	created := &runningFunctionState{}
	actual, _ := runningFunctionStates.LoadOrStore(s, created)
	return actual.(*runningFunctionState)
}

func cloneFunctionCommand(args [][]byte) [][]byte {
	out := make([][]byte, len(args))
	for i, arg := range args {
		out[i] = append([]byte(nil), arg...)
	}
	return out
}

func beginRunningFunction(s *Server, args [][]byte) func() {
	if len(args) < 3 || !isFunctionCallCommand(args) {
		return func() {}
	}
	name := string(args[1])
	if fn := functionRegistryForServer(s).lookup(name); fn != nil {
		name = fn.name
	}
	ctx, cancel := context.WithCancel(context.Background())
	entry := &runningFunction{
		name:    name,
		command: cloneFunctionCommand(args),
		started: time.Now(),
		ctx:     ctx,
		cancel:  cancel,
	}
	state := runningFunctionStateForServer(s)
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

func runningFunctionContext(s *Server) context.Context {
	state := runningFunctionStateForServer(s)
	state.mu.RLock()
	defer state.mu.RUnlock()
	if state.active == nil {
		return context.Background()
	}
	return state.active.ctx
}

func runningFunctionWasKilled(s *Server) bool {
	state := runningFunctionStateForServer(s)
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.active != nil && state.active.killed
}

func isFunctionStatsCommand(args [][]byte) bool {
	return len(args) >= 2 && strings.EqualFold(string(args[0]), "FUNCTION") && strings.EqualFold(string(args[1]), "STATS")
}

func isFunctionHelpCommand(args [][]byte) bool {
	return len(args) >= 2 && strings.EqualFold(string(args[0]), "FUNCTION") && strings.EqualFold(string(args[1]), "HELP")
}

func (s *Server) executeFunctionStats(args [][]byte) ([]byte, error) {
	if len(args) != 2 {
		return nil, errors.New("ERR wrong number of arguments for 'function|stats' command")
	}

	registry := functionRegistryForServer(s)
	registry.mu.RLock()
	librariesCount := len(registry.libraries)
	functionsCount := len(registry.functions)
	registry.mu.RUnlock()

	state := runningFunctionStateForServer(s)
	state.mu.RLock()
	active := state.active
	var running []byte
	if active == nil {
		running = nullBulk()
	} else {
		name := active.name
		command := cloneFunctionCommand(active.command)
		started := active.started
		commandReply := make([][]byte, 0, len(command))
		for _, arg := range command {
			commandReply = append(commandReply, formatBulkString(arg))
		}
		duration := time.Since(started).Milliseconds()
		if duration < 0 {
			duration = 0
		}
		running = array(
			formatBulkString([]byte("name")), formatBulkString([]byte(name)),
			formatBulkString([]byte("command")), array(commandReply...),
			formatBulkString([]byte("duration_ms")), integer(duration),
		)
	}
	state.mu.RUnlock()

	return array(
		formatBulkString([]byte("running_script")), running,
		formatBulkString([]byte("engines")), array(
			formatBulkString([]byte("LUA")), array(
				formatBulkString([]byte("libraries_count")), integer(int64(librariesCount)),
				formatBulkString([]byte("functions_count")), integer(int64(functionsCount)),
			),
		),
	), nil
}

var functionHelpLines = []string{
	"FUNCTION <subcommand> [<arg> [value] [opt] ...]. Subcommands are:",
	"LOAD [REPLACE] <FUNCTION CODE>",
	"    Create a new library with the given library name and code.",
	"DELETE <LIBRARY NAME>",
	"    Delete the given library.",
	"LIST [LIBRARYNAME PATTERN] [WITHCODE]",
	"    Return general information on all the libraries:",
	"    * Library name",
	"    * The engine used to run the Library",
	"    * Functions list",
	"    * Library code (if WITHCODE is given)",
	"    It also possible to get only function that matches a pattern using LIBRARYNAME argument.",
	"STATS",
	"    Return information about the current function running:",
	"    * Function name",
	"    * Command used to run the function",
	"    * Duration in MS that the function is running",
	"    If no function is running, return nil",
	"    In addition, returns a list of available engines.",
	"KILL",
	"    Kill the current running function.",
	"FLUSH [ASYNC|SYNC]",
	"    Delete all the libraries.",
	"    When called without the optional mode argument, the behavior is determined by the",
	"    lazyfree-lazy-user-flush configuration directive. Valid modes are:",
	"    * ASYNC: Asynchronously flush the libraries.",
	"    * SYNC: Synchronously flush the libraries.",
	"DUMP",
	"    Return a serialized payload representing the current libraries, can be restored using FUNCTION RESTORE command",
	"RESTORE <PAYLOAD> [FLUSH|APPEND|REPLACE]",
	"    Restore the libraries represented by the given payload, it is possible to give a restore policy to",
	"    control how to handle existing libraries (default APPEND):",
	"    * FLUSH: delete all existing libraries.",
	"    * APPEND: appends the restored libraries to the existing libraries. On collision, abort.",
	"    * REPLACE: appends the restored libraries to the existing libraries, On collision, replace the old",
	"      libraries with the new libraries (notice that even on this option there is a chance of failure",
	"      in case of functions name collision with functions from another library).",
	"HELP",
	"    Prints this help.",
}

func (s *Server) executeFunctionHelp(args [][]byte) ([]byte, error) {
	if len(args) != 2 {
		return nil, errors.New("ERR wrong number of arguments for 'function|help' command")
	}
	items := make([][]byte, 0, len(functionHelpLines))
	for _, line := range functionHelpLines {
		items = append(items, formatBulkString([]byte(line)))
	}
	return array(items...), nil
}

func (s *Server) executeFunctionIntrospection(args [][]byte) ([]byte, bool, error) {
	if isFunctionStatsCommand(args) {
		response, err := s.executeFunctionStats(args)
		return response, true, err
	}
	if isFunctionHelpCommand(args) {
		response, err := s.executeFunctionHelp(args)
		return response, true, err
	}
	if isFunctionKillCommand(args) {
		response, err := s.executeFunctionKill(args)
		return response, true, err
	}
	return nil, false, nil
}
