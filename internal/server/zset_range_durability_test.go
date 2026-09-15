package server

import (
	"path/filepath"
	"testing"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

func TestZSetRangeAOFRestartRecovery(t *testing.T) {
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

	execute(t, srv, "ZADD", "z", "1", "a", "2", "b", "3", "c", "4", "d", "5", "e")
	execute(t, srv, "PEXPIRE", "z", "60000")
	if got := execute(t, srv, "ZINCRBY", "z", "1.5", "a"); got != "$3\r\n2.5\r\n" {
		t.Fatalf("ZINCRBY=%q", got)
	}
	if got := execute(t, srv, "ZREMRANGEBYSCORE", "z", "(3", "+inf"); got != ":2\r\n" {
		t.Fatalf("ZREMRANGEBYSCORE=%q", got)
	}
	if got := execute(t, srv, "ZREMRANGEBYRANK", "z", "-1", "-1"); got != ":1\r\n" {
		t.Fatalf("ZREMRANGEBYRANK=%q", got)
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
	if len(items) != 2 || string(items[0].Member) != "b" || items[0].Score != 2 || string(items[1].Member) != "a" || items[1].Score != 2.5 {
		t.Fatalf("items=%+v", items)
	}
	if ttl := restarted.TTL("z", true); ttl <= 0 || ttl > 60000 {
		t.Fatalf("TTL after restart=%d", ttl)
	}
}
