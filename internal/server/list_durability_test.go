package server

import (
	"path/filepath"
	"testing"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

func TestListAOFRestartRecovery(t *testing.T) {
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
	if got := execute(t, srv, "RPUSH", "list", "a", "b", "b", "c", "d"); got != ":5\r\n" {
		t.Fatalf("RPUSH=%q", got)
	}
	if got := execute(t, srv, "PEXPIRE", "list", "60000"); got != ":1\r\n" {
		t.Fatalf("PEXPIRE=%q", got)
	}
	if got := execute(t, srv, "LPUSHX", "list", "z"); got != ":6\r\n" {
		t.Fatalf("LPUSHX=%q", got)
	}
	if got := execute(t, srv, "RPUSHX", "list", "tail"); got != ":7\r\n" {
		t.Fatalf("RPUSHX=%q", got)
	}
	if got := execute(t, srv, "LSET", "list", "-1", "e"); got != "+OK\r\n" {
		t.Fatalf("LSET=%q", got)
	}
	if got := execute(t, srv, "LINSERT", "list", "AFTER", "b", "x"); got != ":8\r\n" {
		t.Fatalf("LINSERT=%q", got)
	}
	if got := execute(t, srv, "LREM", "list", "1", "b"); got != ":1\r\n" {
		t.Fatalf("LREM=%q", got)
	}
	if got := execute(t, srv, "LTRIM", "list", "1", "-2"); got != "+OK\r\n" {
		t.Fatalf("LTRIM=%q", got)
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

	items, err := restarted.ListRange("list", 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "x", "b", "c", "d"}
	if len(items) != len(want) {
		t.Fatalf("len=%d want=%d", len(items), len(want))
	}
	for i := range want {
		if string(items[i]) != want[i] {
			t.Fatalf("item[%d]=%q want=%q", i, items[i], want[i])
		}
	}
	if got := restarted.Type("list"); got != "list" {
		t.Fatalf("type after restart=%q", got)
	}
	if ttl := restarted.TTL("list", true); ttl <= 0 || ttl > 60000 {
		t.Fatalf("LIST TTL after restart=%d", ttl)
	}
}
