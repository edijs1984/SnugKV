package server

import (
	"errors"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func waitForZSetWaiter(t *testing.T, s *Server, key string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		registry := zsetRegistryForServer(s)
		registry.mu.Lock()
		count := len(registry.byKey[key])
		registry.mu.Unlock()
		if count > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("no blocking zset waiter registered for %q", key)
}

func TestBlockingZPopImmediateRepliesAndTimeout(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "ZADD", "scores", "1", "a", "2", "b")

	if got := execute(t, s, "BZPOPMIN", "scores", "1"); got != "*3\r\n$6\r\nscores\r\n$1\r\na\r\n$1\r\n1\r\n" {
		t.Fatalf("BZPOPMIN=%q", got)
	}
	if got := execute(t, s, "BZPOPMAX", "scores", "1"); got != "*3\r\n$6\r\nscores\r\n$1\r\nb\r\n$1\r\n2\r\n" {
		t.Fatalf("BZPOPMAX=%q", got)
	}

	started := time.Now()
	if got := execute(t, s, "BZPOPMIN", "missing", "0.02"); got != "*-1\r\n" {
		t.Fatalf("timeout BZPOPMIN=%q", got)
	}
	if time.Since(started) < 10*time.Millisecond {
		t.Fatalf("BZPOPMIN timeout returned too early: %s", time.Since(started))
	}
}

func TestBlockingZPopWakesOnZAddAndKeepsKeyPriority(t *testing.T) {
	s := New(engine.New())
	blocked := executeAsync(s, "BZPOPMIN", "first", "second", "1")
	waitForZSetWaiter(t, s, "second")

	if got := execute(t, s, "ZADD", "second", "2", "value"); got != ":1\r\n" {
		t.Fatalf("ZADD=%q", got)
	}
	select {
	case result := <-blocked:
		if result.err != nil {
			t.Fatal(result.err)
		}
		want := "*3\r\n$6\r\nsecond\r\n$5\r\nvalue\r\n$1\r\n2\r\n"
		if got := string(result.response); got != want {
			t.Fatalf("BZPOPMIN wake=%q want=%q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("BZPOPMIN did not wake")
	}

	execute(t, s, "ZADD", "first", "1", "a")
	execute(t, s, "ZADD", "second", "0", "b")
	want := "*3\r\n$5\r\nfirst\r\n$1\r\na\r\n$1\r\n1\r\n"
	if got := execute(t, s, "BZPOPMIN", "first", "second", "1"); got != want {
		t.Fatalf("key priority=%q want=%q", got, want)
	}
}

func TestBZMPOPWakesAndReturnsNestedPairs(t *testing.T) {
	s := New(engine.New())
	blocked := executeAsync(s, "BZMPOP", "1", "2", "first", "second", "MAX", "COUNT", "2")
	waitForZSetWaiter(t, s, "second")

	execute(t, s, "ZADD", "second", "1", "x", "2", "y", "3", "z")
	select {
	case result := <-blocked:
		if result.err != nil {
			t.Fatal(result.err)
		}
		want := "*2\r\n$6\r\nsecond\r\n*2\r\n*2\r\n$1\r\nz\r\n$1\r\n3\r\n*2\r\n$1\r\ny\r\n$1\r\n2\r\n"
		if got := string(result.response); got != want {
			t.Fatalf("BZMPOP=%q want=%q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("BZMPOP did not wake")
	}

	if got := execute(t, s, "ZRANGE", "second", "0", "-1", "WITHSCORES"); got != "*2\r\n$1\r\nx\r\n$1\r\n1\r\n" {
		t.Fatalf("remaining=%q", got)
	}
}

func TestBlockingZSetCancelReleasesInfiniteWait(t *testing.T) {
	s := New(engine.New())
	blocked := executeAsync(s, "BZPOPMAX", "forever", "0")
	waitForZSetWaiter(t, s, "forever")
	s.CancelBlockingZSets()

	select {
	case result := <-blocked:
		if !errors.Is(result.err, errBlockingCanceled) {
			t.Fatalf("cancel err=%v", result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("infinite zset blocker was not canceled")
	}
}

func TestBlockingZSetDoesNotHoldDurabilityLockWhileWaiting(t *testing.T) {
	s := New(engine.New())
	journal := &countingJournal{}
	s.SetJournal(journal)

	blocked := executeAsync(s, "BZPOPMIN", "queue", "1")
	waitForZSetWaiter(t, s, "queue")

	producer := executeAsync(s, "ZADD", "queue", "5", "job")
	select {
	case result := <-producer:
		if result.err != nil || string(result.response) != ":1\r\n" {
			t.Fatalf("producer response=%q err=%v", result.response, result.err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("producer blocked behind sleeping BZPOPMIN durability lock")
	}

	select {
	case result := <-blocked:
		want := "*3\r\n$5\r\nqueue\r\n$3\r\njob\r\n$1\r\n5\r\n"
		if result.err != nil || string(result.response) != want {
			t.Fatalf("blocked response=%q err=%v", result.response, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("BZPOPMIN did not consume produced value")
	}

	if calls := journal.Calls(); calls != 2 {
		t.Fatalf("journal appends=%d want 2", calls)
	}
}

func TestBlockingZSetValidation(t *testing.T) {
	s := New(engine.New())
	for _, command := range [][]string{
		{"BZPOPMIN", "key", "-1"},
		{"BZPOPMAX", "key", "nan"},
		{"BZMPOP", "1", "0", "MIN"},
		{"BZMPOP", "1", "1", "key", "UP"},
		{"BZMPOP", "1", "1", "key", "MIN", "COUNT", "0"},
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
