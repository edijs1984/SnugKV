package server

import (
	"errors"
	"sync"
	"testing"
	"time"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

type blockingTestResult struct {
	response []byte
	err      error
}

func waitForListWaiter(t *testing.T, s *Server, key string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		registry := registryForServer(s)
		registry.mu.Lock()
		count := len(registry.byKey[key])
		registry.mu.Unlock()
		if count > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("no blocking waiter registered for %q", key)
}

func executeAsync(s *Server, args ...string) <-chan blockingTestResult {
	out := make(chan blockingTestResult, 1)
	command := make([][]byte, len(args))
	for i := range args {
		command[i] = []byte(args[i])
	}
	go func() {
		response, err := s.Execute(command)
		out <- blockingTestResult{response: response, err: err}
	}()
	return out
}

func TestBlockingListImmediateRepliesAndTimeout(t *testing.T) {
	s := New(engine.New())
	if got := execute(t, s, "RPUSH", "list", "a", "b"); got != ":2\r\n" {
		t.Fatalf("RPUSH=%q", got)
	}
	if got := execute(t, s, "BLPOP", "list", "1"); got != "*2\r\n$4\r\nlist\r\n$1\r\na\r\n" {
		t.Fatalf("BLPOP=%q", got)
	}
	if got := execute(t, s, "BRPOP", "list", "1"); got != "*2\r\n$4\r\nlist\r\n$1\r\nb\r\n" {
		t.Fatalf("BRPOP=%q", got)
	}

	started := time.Now()
	if got := execute(t, s, "BLPOP", "missing", "0.02"); got != "*-1\r\n" {
		t.Fatalf("timeout BLPOP=%q", got)
	}
	if time.Since(started) < 10*time.Millisecond {
		t.Fatalf("BLPOP timeout returned too early: %s", time.Since(started))
	}
}

func TestBlockingPopWakesOnRelevantPushAndKeepsKeyPriority(t *testing.T) {
	s := New(engine.New())
	blocked := executeAsync(s, "BLPOP", "first", "second", "1")
	waitForListWaiter(t, s, "second")

	if got := execute(t, s, "RPUSH", "second", "value"); got != ":1\r\n" {
		t.Fatalf("RPUSH=%q", got)
	}
	select {
	case result := <-blocked:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if got := string(result.response); got != "*2\r\n$6\r\nsecond\r\n$5\r\nvalue\r\n" {
			t.Fatalf("BLPOP wake=%q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("BLPOP did not wake")
	}

	// When both keys are ready before the command starts, key order wins.
	execute(t, s, "RPUSH", "first", "a")
	execute(t, s, "RPUSH", "second", "b")
	if got := execute(t, s, "BRPOP", "first", "second", "1"); got != "*2\r\n$5\r\nfirst\r\n$1\r\na\r\n" {
		t.Fatalf("BRPOP priority=%q", got)
	}
}

func TestBLMoveAndBRPopLPushWakeAndMove(t *testing.T) {
	s := New(engine.New())
	blocked := executeAsync(s, "BLMOVE", "source", "dest", "RIGHT", "LEFT", "1")
	waitForListWaiter(t, s, "source")
	execute(t, s, "RPUSH", "source", "a", "b")

	select {
	case result := <-blocked:
		if result.err != nil || string(result.response) != "$1\r\nb\r\n" {
			t.Fatalf("BLMOVE response=%q err=%v", result.response, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("BLMOVE did not wake")
	}
	if got := execute(t, s, "LRANGE", "source", "0", "-1"); got != "*1\r\n$1\r\na\r\n" {
		t.Fatalf("source=%q", got)
	}
	if got := execute(t, s, "LRANGE", "dest", "0", "-1"); got != "*1\r\n$1\r\nb\r\n" {
		t.Fatalf("dest=%q", got)
	}

	legacy := executeAsync(s, "BRPOPLPUSH", "legacy:src", "legacy:dst", "1")
	waitForListWaiter(t, s, "legacy:src")
	execute(t, s, "RPUSH", "legacy:src", "x")
	select {
	case result := <-legacy:
		if result.err != nil || string(result.response) != "$1\r\nx\r\n" {
			t.Fatalf("BRPOPLPUSH response=%q err=%v", result.response, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("BRPOPLPUSH did not wake")
	}
}

func TestBlockingListCancelReleasesInfiniteWait(t *testing.T) {
	s := New(engine.New())
	blocked := executeAsync(s, "BLPOP", "forever", "0")
	waitForListWaiter(t, s, "forever")
	s.CancelBlocking()

	select {
	case result := <-blocked:
		if !errors.Is(result.err, errBlockingCanceled) {
			t.Fatalf("cancel err=%v", result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("infinite blocker was not canceled")
	}
}

type countingJournal struct {
	mu    sync.Mutex
	calls int
}

func (j *countingJournal) Append(_ []persistence.Record) error {
	j.mu.Lock()
	j.calls++
	j.mu.Unlock()
	return nil
}

func (j *countingJournal) Calls() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.calls
}

func TestBlockingListDoesNotHoldDurabilityLockWhileWaiting(t *testing.T) {
	s := New(engine.New())
	journal := &countingJournal{}
	s.SetJournal(journal)

	blocked := executeAsync(s, "BLPOP", "queue", "1")
	waitForListWaiter(t, s, "queue")

	producer := executeAsync(s, "RPUSH", "queue", "job")
	select {
	case result := <-producer:
		if result.err != nil || string(result.response) != ":1\r\n" {
			t.Fatalf("producer response=%q err=%v", result.response, result.err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("producer blocked behind sleeping BLPOP durability lock")
	}

	select {
	case result := <-blocked:
		if result.err != nil || string(result.response) != "*2\r\n$5\r\nqueue\r\n$3\r\njob\r\n" {
			t.Fatalf("blocked response=%q err=%v", result.response, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("BLPOP did not consume produced value")
	}

	// Empty readiness checks are read-only: only RPUSH and the eventual LPOP
	// should append durable state.
	if calls := journal.Calls(); calls != 2 {
		t.Fatalf("journal appends=%d want 2", calls)
	}
}

func TestBlockingListValidation(t *testing.T) {
	s := New(engine.New())
	for _, command := range [][]string{
		{"BLPOP", "key", "-1"},
		{"BRPOP", "key", "nan"},
		{"BLMOVE", "a", "b", "UP", "LEFT", "1"},
		{"BLMOVE", "a", "b", "LEFT", "DOWN", "1"},
	} {
		args := make([][]byte, len(command))
		for i := range command {
			args[i] = []byte(command[i])
		}
		if _, err := s.Execute(args); err == nil {
			t.Fatalf("expected error for %v", command)
		}
	}
}
