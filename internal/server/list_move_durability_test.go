package server

import (
	"path/filepath"
	"testing"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

func TestListMoveAOFRestartRecovery(t *testing.T) {
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
	execute(t, srv, "RPUSH", "source", "a", "b", "c")
	execute(t, srv, "RPUSH", "dest", "x", "y")
	execute(t, srv, "PEXPIRE", "source", "60000")
	execute(t, srv, "PEXPIRE", "dest", "120000")

	if got := execute(t, srv, "LMOVE", "source", "dest", "RIGHT", "LEFT"); got != "$1\r\nc\r\n" {
		t.Fatalf("LMOVE=%q", got)
	}
	if got := execute(t, srv, "RPOPLPUSH", "source", "dest"); got != "$1\r\nb\r\n" {
		t.Fatalf("RPOPLPUSH=%q", got)
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

	source, err := restarted.ListRange("source", 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(source) != 1 || string(source[0]) != "a" {
		t.Fatalf("source after restart=%q", source)
	}
	dest, err := restarted.ListRange("dest", 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"b", "c", "x", "y"}
	if len(dest) != len(want) {
		t.Fatalf("dest len=%d want=%d", len(dest), len(want))
	}
	for i := range want {
		if string(dest[i]) != want[i] {
			t.Fatalf("dest[%d]=%q want=%q", i, dest[i], want[i])
		}
	}
	if ttl := restarted.TTL("source", true); ttl <= 0 || ttl > 60000 {
		t.Fatalf("source TTL after restart=%d", ttl)
	}
	if ttl := restarted.TTL("dest", true); ttl <= 0 || ttl > 120000 {
		t.Fatalf("dest TTL after restart=%d", ttl)
	}
}
