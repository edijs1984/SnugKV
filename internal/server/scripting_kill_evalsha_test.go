package server

import (
	"strings"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func TestScriptKillCancelsRunningEvalSHA(t *testing.T) {
	s := New(engine.New())
	shaReply := execute(t, s, "SCRIPT", "LOAD", "while true do end")
	sha := strings.TrimSuffix(strings.TrimPrefix(shaReply, "$40\r\n"), "\r\n")
	if len(sha) != 40 {
		t.Fatalf("unexpected SCRIPT LOAD reply %q", shaReply)
	}

	done := make(chan error, 1)
	go func() {
		_, err := s.Execute([][]byte{[]byte("EVALSHA"), []byte(sha), []byte("0")})
		done <- err
	}()

	waitForRunningScript(t, s, func(active *runningScript) bool { return !active.writeDirty })
	if got := execute(t, s, "SCRIPT", "KILL"); got != "+OK\r\n" {
		t.Fatalf("SCRIPT KILL EVALSHA = %q", got)
	}

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "Script killed by user with SCRIPT KILL") {
			t.Fatalf("killed EVALSHA error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("killed EVALSHA did not stop")
	}
}
