package server

import (
	"errors"
	"fmt"
	"strings"

	lua "github.com/yuin/gopher-lua"
)

var errFunctionKilled = errors.New("ERR Script killed by user with SCRIPT KILL...")

func isFunctionKillCommand(args [][]byte) bool {
	return len(args) >= 2 && strings.EqualFold(string(args[0]), "FUNCTION") && strings.EqualFold(string(args[1]), "KILL")
}

func scriptCommandWritesDataset(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	cmd := strings.ToUpper(string(args[0]))
	if cmd == "SORT" {
		_, store := sortStoreDestination(args)
		return store
	}
	info, ok := commandTable[cmd]
	return ok && info.write
}

// markRunningFunctionWrite closes the race between FUNCTION KILL and a nested
// write. The same state mutex serializes both transitions: either KILL marks the
// invocation killed first and the write is rejected, or the write marks the
// invocation dirty first and KILL becomes UNKILLABLE.
func markRunningFunctionWrite(s *Server) error {
	state := runningFunctionStateForServer(s)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.active == nil {
		return nil
	}
	if state.active.killed {
		return errFunctionKilled
	}
	state.active.writeDirty = true
	return nil
}

// luaRedisCallFunction mirrors the ordinary Lua bridge but gates dataset writes
// through the running-function state before dispatch. That gate is what makes
// FUNCTION KILL atomic with respect to the first write.
func (s *Server) luaRedisCallFunction(protected bool) lua.LGFunction {
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
		if err := queuedCommandValidation(args); err != nil {
			if strings.Contains(err.Error(), "wrong number of arguments") {
				err = errors.New("ERR Wrong number of args calling Redis command from script")
			}
			return luaPushCommandError(L, protected, err)
		}
		if scriptCommandWritesDataset(args) {
			if err := markRunningFunctionWrite(s); err != nil {
				return luaPushCommandError(L, protected, err)
			}
		}

		result, err := s.executePressureMode(args, false)
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

func (s *Server) executeFunctionKill(args [][]byte) ([]byte, error) {
	if len(args) != 2 {
		return nil, errors.New("ERR wrong number of arguments for 'function|kill' command")
	}

	state := runningFunctionStateForServer(s)
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
