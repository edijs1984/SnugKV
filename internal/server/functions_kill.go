package server

import (
	"errors"
	"strings"
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
		// EVAL uses the same Lua bridge but is not governed by FUNCTION KILL.
		return nil
	}
	if state.active.killed {
		return errFunctionKilled
	}
	state.active.writeDirty = true
	return nil
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
