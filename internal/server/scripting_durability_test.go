package server

import (
	"path/filepath"
	"strings"
	"testing"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

func TestEvalRuntimeErrorPersistsEarlierWritesInOneFrame(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.snug")
	journal, err := persistence.Open(path, "always")
	if err != nil {
		t.Fatal(err)
	}

	store := engine.New()
	s := New(store)
	s.SetJournal(journal)

	script := "redis.call('SET',KEYS[1],'one'); redis.call('SET',KEYS[2],'two'); error('boom')"
	args := [][]byte{[]byte("EVAL"), []byte(script), []byte("2"), []byte("lua:a"), []byte("lua:b")}
	if _, err := s.Execute(args); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("EVAL runtime error = %v", err)
	}
	if got := execute(t, s, "GET", "lua:a"); got != "$3\r\none\r\n" {
		t.Fatalf("lua:a before restart = %q", got)
	}
	if got := execute(t, s, "GET", "lua:b"); got != "$3\r\ntwo\r\n" {
		t.Fatalf("lua:b before restart = %q", got)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := engine.New()
	frames := 0
	if err := persistence.Replay(path, func(records []persistence.Record) error {
		frames++
		return restarted.Restore(records, false)
	}); err != nil {
		t.Fatal(err)
	}
	if frames != 1 {
		t.Fatalf("script persistence frames = %d, want 1", frames)
	}
	if value, ok := restarted.Get("lua:a"); !ok || string(value) != "one" {
		t.Fatalf("lua:a after restart = %q, %v", value, ok)
	}
	if value, ok := restarted.Get("lua:b"); !ok || string(value) != "two" {
		t.Fatalf("lua:b after restart = %q, %v", value, ok)
	}
}

func TestEvalInsideMultiExec(t *testing.T) {
	s := New(engine.New())
	session := newTransactionSession(s)
	defer session.close()

	if handled, got, err := session.handleCommand([][]byte{[]byte("MULTI")}); !handled || err != nil || string(got) != "+OK\r\n" {
		t.Fatalf("MULTI handled=%v response=%q err=%v", handled, got, err)
	}
	script := "redis.call('SET',KEYS[1],ARGV[1]); return redis.call('GET',KEYS[1])"
	if handled, got, err := session.handleCommand([][]byte{[]byte("EVAL"), []byte(script), []byte("1"), []byte("tx:lua"), []byte("value")}); !handled || err != nil || string(got) != "+QUEUED\r\n" {
		t.Fatalf("queue EVAL handled=%v response=%q err=%v", handled, got, err)
	}
	if handled, got, err := session.handleCommand([][]byte{[]byte("EXEC")}); !handled || err != nil || string(got) != "*1\r\n$5\r\nvalue\r\n" {
		t.Fatalf("EXEC handled=%v response=%q err=%v", handled, got, err)
	}
	if got := execute(t, s, "GET", "tx:lua"); got != "$5\r\nvalue\r\n" {
		t.Fatalf("GET after EXEC = %q", got)
	}
}

func TestEvalTransientMutationInvalidatesWatch(t *testing.T) {
	s := New(engine.New())
	if got := execute(t, s, "SET", "watched:lua", "original"); got != "+OK\r\n" {
		t.Fatalf("SET = %q", got)
	}

	watcher := newTransactionSession(s)
	defer watcher.close()
	if handled, got, err := watcher.handleCommand([][]byte{[]byte("WATCH"), []byte("watched:lua")}); !handled || err != nil || string(got) != "+OK\r\n" {
		t.Fatalf("WATCH handled=%v response=%q err=%v", handled, got, err)
	}

	script := "redis.call('SET',KEYS[1],'temporary'); redis.call('SET',KEYS[1],'original'); return 1"
	if got := execute(t, s, "EVAL", script, "1", "watched:lua"); got != ":1\r\n" {
		t.Fatalf("EVAL = %q", got)
	}
	if got := execute(t, s, "GET", "watched:lua"); got != "$8\r\noriginal\r\n" {
		t.Fatalf("restored value = %q", got)
	}

	if handled, got, err := watcher.handleCommand([][]byte{[]byte("MULTI")}); !handled || err != nil || string(got) != "+OK\r\n" {
		t.Fatalf("MULTI handled=%v response=%q err=%v", handled, got, err)
	}
	if handled, got, err := watcher.handleCommand([][]byte{[]byte("GET"), []byte("watched:lua")}); !handled || err != nil || string(got) != "+QUEUED\r\n" {
		t.Fatalf("queue GET handled=%v response=%q err=%v", handled, got, err)
	}
	if handled, got, err := watcher.handleCommand([][]byte{[]byte("EXEC")}); !handled || err != nil || string(got) != "*-1\r\n" {
		t.Fatalf("EXEC after transient mutation handled=%v response=%q err=%v", handled, got, err)
	}
}
