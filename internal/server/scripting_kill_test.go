package server

import (
	"strings"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func waitForRunningScript(t *testing.T, s *Server, predicate func(*runningScript) bool) *runningScript {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state := runningScriptStateForServer(s)
		state.mu.RLock()
		active := state.active
		matched := active != nil && predicate(active)
		state.mu.RUnlock()
		if matched {
			return active
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for running script state")
	return nil
}

func TestScriptKillNotBusy(t *testing.T) {
	s := New(engine.New())
	_, err := s.Execute([][]byte{[]byte("SCRIPT"), []byte("KILL")})
	if err == nil || err.Error() != "NOTBUSY No scripts in execution right now." {
		t.Fatalf("SCRIPT KILL idle error = %v", err)
	}
}

func TestScriptKillCancelsRunningEval(t *testing.T) {
	s := New(engine.New())
	done := make(chan error, 1)
	go func() {
		_, err := s.Execute([][]byte{[]byte("EVAL"), []byte("while true do end"), []byte("0")})
		done <- err
	}()

	waitForRunningScript(t, s, func(active *runningScript) bool { return !active.writeDirty })
	if got := execute(t, s, "SCRIPT", "KILL"); got != "+OK\r\n" {
		t.Fatalf("SCRIPT KILL = %q", got)
	}

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "Script killed by user with SCRIPT KILL") {
			t.Fatalf("killed EVAL error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("killed EVAL did not stop")
	}
}

func TestScriptKillCancelsReadOnlyEval(t *testing.T) {
	s := New(engine.New())
	done := make(chan error, 1)
	go func() {
		_, err := s.Execute([][]byte{[]byte("EVAL_RO"), []byte("while true do end"), []byte("0")})
		done <- err
	}()

	waitForRunningScript(t, s, func(active *runningScript) bool { return !active.writeDirty })
	if got := execute(t, s, "SCRIPT", "KILL"); got != "+OK\r\n" {
		t.Fatalf("SCRIPT KILL EVAL_RO = %q", got)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "Script killed by user with SCRIPT KILL") {
			t.Fatalf("killed EVAL_RO error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("killed EVAL_RO did not stop")
	}
}

func TestScriptKillBecomesUnkillableAfterWriteBoundary(t *testing.T) {
	s := New(engine.New())
	done := make(chan error, 1)
	go func() {
		_, err := s.Execute([][]byte{
			[]byte("EVAL"),
			[]byte("redis.call('SET',KEYS[1],'1'); while true do end"),
			[]byte("1"),
			[]byte("script:dirty"),
		})
		done <- err
	}()

	active := waitForRunningScript(t, s, func(active *runningScript) bool { return active.writeDirty })
	_, err := s.Execute([][]byte{[]byte("SCRIPT"), []byte("KILL")})
	if err == nil || !strings.HasPrefix(err.Error(), "UNKILLABLE Sorry the script already executed write commands against the dataset.") {
		t.Fatalf("SCRIPT KILL dirty error = %v", err)
	}

	active.cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("dirty EVAL unexpectedly completed successfully")
		}
	case <-time.After(time.Second):
		t.Fatal("dirty EVAL did not stop after test cleanup")
	}
	if got := execute(t, s, "GET", "script:dirty"); got != "$1\r\n1\r\n" {
		t.Fatalf("write before unkillable state missing: %q", got)
	}
}

func TestScriptKillArity(t *testing.T) {
	s := New(engine.New())
	_, err := s.Execute([][]byte{[]byte("SCRIPT"), []byte("KILL"), []byte("extra")})
	if err == nil || err.Error() != "ERR wrong number of arguments for 'script|kill' command" {
		t.Fatalf("SCRIPT KILL arity error = %v", err)
	}
}
