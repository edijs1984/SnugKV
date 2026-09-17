package server

import (
	"strings"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func waitForRunningFunction(t *testing.T, s *Server, predicate func(*runningFunction) bool) *runningFunction {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state := runningFunctionStateForServer(s)
		state.mu.RLock()
		active := state.active
		matched := active != nil && predicate(active)
		state.mu.RUnlock()
		if matched {
			return active
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for running function state")
	return nil
}

func TestFunctionKillNotBusy(t *testing.T) {
	s := New(engine.New())
	_, err := s.Execute([][]byte{[]byte("FUNCTION"), []byte("KILL")})
	if err == nil || err.Error() != "NOTBUSY No scripts in execution right now." {
		t.Fatalf("FUNCTION KILL idle error = %v", err)
	}
}

func TestFunctionKillCancelsRunningReadOnlyFunction(t *testing.T) {
	s := New(engine.New())
	loadFunctionLibrary(t, s, "#!lua name=killable\nredis.register_function{function_name='spin',callback=function(keys,args) while true do end end,flags={'no-writes'}}")

	done := make(chan error, 1)
	go func() {
		_, err := s.Execute([][]byte{[]byte("FCALL"), []byte("spin"), []byte("0")})
		done <- err
	}()

	waitForRunningFunction(t, s, func(active *runningFunction) bool { return active.name == "spin" })
	if got := execute(t, s, "FUNCTION", "KILL"); got != "+OK\r\n" {
		t.Fatalf("FUNCTION KILL = %q", got)
	}

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "Script killed by user with SCRIPT KILL") {
			t.Fatalf("killed FCALL error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("killed FCALL did not stop")
	}
}

func TestFunctionKillBecomesUnkillableAfterWriteBoundary(t *testing.T) {
	s := New(engine.New())
	loadFunctionLibrary(t, s, "#!lua name=dirty\nredis.register_function('dirtyspin', function(keys,args) redis.call('SET',keys[1],'1'); while true do end end)")

	done := make(chan error, 1)
	go func() {
		_, err := s.Execute([][]byte{[]byte("FCALL"), []byte("dirtyspin"), []byte("1"), []byte("fn:dirty")})
		done <- err
	}()

	active := waitForRunningFunction(t, s, func(active *runningFunction) bool { return active.name == "dirtyspin" && active.writeDirty })
	_, err := s.Execute([][]byte{[]byte("FUNCTION"), []byte("KILL")})
	if err == nil || !strings.HasPrefix(err.Error(), "UNKILLABLE Sorry the script already executed write commands against the dataset.") {
		t.Fatalf("FUNCTION KILL dirty error = %v", err)
	}

	// The command is deliberately unkillable through FUNCTION KILL. Cancel its
	// private context directly so this unit test does not wait for the timeout.
	active.cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("dirty FCALL unexpectedly completed successfully")
		}
	case <-time.After(time.Second):
		t.Fatal("dirty FCALL did not stop after test cleanup")
	}
	if got := execute(t, s, "GET", "fn:dirty"); got != "$1\r\n1\r\n" {
		t.Fatalf("write before unkillable state missing: %q", got)
	}
}

func TestFunctionKillArity(t *testing.T) {
	s := New(engine.New())
	_, err := s.Execute([][]byte{[]byte("FUNCTION"), []byte("KILL"), []byte("extra")})
	if err == nil || err.Error() != "ERR wrong number of arguments for 'function|kill' command" {
		t.Fatalf("FUNCTION KILL arity error = %v", err)
	}
}
