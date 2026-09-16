package server

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

func TestCopyPersistsDestinationAndTTL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.snug")
	journal, err := persistence.Open(path, "always")
	if err != nil {
		t.Fatal(err)
	}

	store := engine.New()
	s := New(store)
	s.SetJournal(journal)
	executeCopyTest(t, s, "RPUSH", "copy:src", "a", "b")
	executeCopyTest(t, s, "PEXPIRE", "copy:src", "60000")
	if got, err := executeCopyTest(t, s, "COPY", "copy:src", "copy:dst"); err != nil || got != ":1\r\n" {
		t.Fatalf("COPY = %q, %v", got, err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := engine.New()
	if err := persistence.Replay(path, func(records []persistence.Record) error {
		return restarted.Restore(records, false)
	}); err != nil {
		t.Fatal(err)
	}
	rs := New(restarted)
	if got, _ := executeCopyTest(t, rs, "TYPE", "copy:src"); got != "+list\r\n" {
		t.Fatalf("source type after restart = %q", got)
	}
	if got, _ := executeCopyTest(t, rs, "TYPE", "copy:dst"); got != "+list\r\n" {
		t.Fatalf("destination type after restart = %q", got)
	}
	if got, _ := executeCopyTest(t, rs, "LRANGE", "copy:dst", "0", "-1"); got != "*2\r\n$1\r\na\r\n$1\r\nb\r\n" {
		t.Fatalf("destination after restart = %q", got)
	}
	got, err := executeCopyTest(t, rs, "PTTL", "copy:dst")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(got, ":"), "\r\n"), 10, 64)
	if err != nil || ms <= 0 || ms > 60000 {
		t.Fatalf("destination TTL after restart = %q", got)
	}
}
