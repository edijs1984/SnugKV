package server

import (
	"path/filepath"
	"testing"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

func TestZSetAOFRestartRecovery(t *testing.T) {
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
	if got := execute(t, srv, "ZADD", "z", "1", "a", "2", "b", "3", "c"); got != ":3\r\n" {
		t.Fatalf("ZADD=%q", got)
	}
	if got := execute(t, srv, "PEXPIRE", "z", "60000"); got != ":1\r\n" {
		t.Fatalf("PEXPIRE=%q", got)
	}
	if got := execute(t, srv, "ZADD", "z", "CH", "4", "a", "2", "b"); got != ":1\r\n" {
		t.Fatalf("ZADD update=%q", got)
	}
	if got := execute(t, srv, "ZREM", "z", "c"); got != ":1\r\n" {
		t.Fatalf("ZREM=%q", got)
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

	items, err := restarted.ZSetRange("z", 0, -1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || string(items[0].Member) != "b" || items[0].Score != 2 || string(items[1].Member) != "a" || items[1].Score != 4 {
		t.Fatalf("items=%+v", items)
	}
	if got := restarted.Type("z"); got != "zset" {
		t.Fatalf("type after restart=%q", got)
	}
	if ttl := restarted.TTL("z", true); ttl <= 0 || ttl > 60000 {
		t.Fatalf("ZSET TTL after restart=%d", ttl)
	}
}
