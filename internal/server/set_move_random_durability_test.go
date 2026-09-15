package server

import (
	"path/filepath"
	"testing"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

func TestSetMoveAndPopAOFRestartRecovery(t *testing.T) {
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
	execute(t, srv, "SADD", "source", "a", "b", "c")
	execute(t, srv, "SADD", "dest", "x")
	execute(t, srv, "PEXPIRE", "source", "60000")
	execute(t, srv, "PEXPIRE", "dest", "120000")

	if got := execute(t, srv, "SMOVE", "source", "dest", "a"); got != ":1\r\n" {
		t.Fatalf("SMOVE=%q", got)
	}
	popped := execute(t, srv, "SPOP", "source")
	if len(popped) == 0 || popped[0] != '$' {
		t.Fatalf("SPOP=%q", popped)
	}

	beforeSource, err := first.SetMembers("source")
	if err != nil {
		t.Fatal(err)
	}
	beforeDest, err := first.SetMembers("dest")
	if err != nil {
		t.Fatal(err)
	}
	if len(beforeSource) != 1 {
		t.Fatalf("source cardinality before restart=%d want 1", len(beforeSource))
	}
	assertServerSetMembers(t, beforeDest, "a", "x")
	if ttl := first.TTL("source", true); ttl <= 0 {
		t.Fatalf("source TTL before restart=%d", ttl)
	}
	if ttl := first.TTL("dest", true); ttl <= 0 {
		t.Fatalf("dest TTL before restart=%d", ttl)
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

	afterSource, err := restarted.SetMembers("source")
	if err != nil {
		t.Fatal(err)
	}
	if len(afterSource) != len(beforeSource) || string(afterSource[0]) != string(beforeSource[0]) {
		t.Fatalf("source after restart=%q before=%q", afterSource, beforeSource)
	}
	afterDest, err := restarted.SetMembers("dest")
	if err != nil {
		t.Fatal(err)
	}
	assertServerSetMembers(t, afterDest, "a", "x")
	if ttl := restarted.TTL("source", true); ttl <= 0 || ttl > 60000 {
		t.Fatalf("source TTL after restart=%d", ttl)
	}
	if ttl := restarted.TTL("dest", true); ttl <= 0 || ttl > 120000 {
		t.Fatalf("dest TTL after restart=%d", ttl)
	}
}
