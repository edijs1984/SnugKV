package server

import (
	"path/filepath"
	"testing"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

func TestSortStorePersistsDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.snug")
	journal, err := persistence.Open(path, "always")
	if err != nil {
		t.Fatal(err)
	}

	store := engine.New()
	s := New(store)
	s.SetJournal(journal)
	execute(t, s, "RPUSH", "sort:src", "3", "1", "2")
	execute(t, s, "SET", "sort:dst", "old")
	execute(t, s, "EXPIRE", "sort:dst", "600")
	if got := execute(t, s, "SORT", "sort:src", "DESC", "STORE", "sort:dst"); got != ":3\r\n" {
		t.Fatalf("SORT STORE = %q", got)
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
	if got := execute(t, rs, "TYPE", "sort:dst"); got != "+list\r\n" {
		t.Fatalf("restarted destination type = %q", got)
	}
	if got := execute(t, rs, "LRANGE", "sort:dst", "0", "-1"); got != "*3\r\n$1\r\n3\r\n$1\r\n2\r\n$1\r\n1\r\n" {
		t.Fatalf("restarted destination = %q", got)
	}
	if got := execute(t, rs, "TTL", "sort:dst"); got != ":-1\r\n" {
		t.Fatalf("restarted destination TTL = %q", got)
	}
}
