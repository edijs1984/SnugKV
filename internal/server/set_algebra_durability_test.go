package server

import (
	"bytes"
	"path/filepath"
	"testing"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

func TestSetAlgebraStoreAOFRestartRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.snug")

	first, err := engine.NewWithOptions(engine.Options{Shards: 16})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := persistence.Open(path, "always")
	if err != nil {
		t.Fatal(err)
	}

	srv := New(first)
	srv.SetJournal(journal)
	execute(t, srv, "SADD", "left", "a", "b", "d")
	execute(t, srv, "SADD", "right", "b", "c", "d")
	execute(t, srv, "SADD", "dest", "old")
	execute(t, srv, "PEXPIRE", "dest", "60000")

	if got := execute(t, srv, "SUNIONSTORE", "dest", "left", "right"); got != ":4\r\n" {
		t.Fatalf("SUNIONSTORE=%q", got)
	}
	if got := execute(t, srv, "SDIFFSTORE", "diff", "left", "right"); got != ":1\r\n" {
		t.Fatalf("SDIFFSTORE=%q", got)
	}

	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := engine.NewWithOptions(engine.Options{Shards: 16})
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.Replay(path, func(records []persistence.Record) error {
		return restarted.Restore(records, false)
	}); err != nil {
		t.Fatal(err)
	}

	dest, err := restarted.SetMembers("dest")
	if err != nil {
		t.Fatal(err)
	}
	assertServerSetMembers(t, dest, "a", "b", "c", "d")
	if ttl := restarted.TTL("dest", true); ttl != -1 {
		t.Fatalf("destination TTL after restart=%d want -1", ttl)
	}

	diff, err := restarted.SetMembers("diff")
	if err != nil {
		t.Fatal(err)
	}
	assertServerSetMembers(t, diff, "a")
}

func assertServerSetMembers(t *testing.T, got [][]byte, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], []byte(want[i])) {
			t.Fatalf("member[%d]=%q want %q", i, got[i], want[i])
		}
	}
}
