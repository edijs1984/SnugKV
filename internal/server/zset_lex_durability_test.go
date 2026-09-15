package server

import (
	"path/filepath"
	"testing"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

func TestZSetLexAOFRestartRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.snug")
	first, err := engine.NewWithOptions(engine.Options{Shards: 16})
	if err != nil { t.Fatal(err) }
	journal, err := persistence.Open(path, "always")
	if err != nil { t.Fatal(err) }
	srv := New(first)
	srv.SetJournal(journal)

	if got := execute(t, srv, "ZADD", "z", "0", "a", "0", "b", "0", "c", "0", "d"); got != ":4\r\n" {
		t.Fatalf("ZADD=%q", got)
	}
	if got := execute(t, srv, "PEXPIRE", "z", "60000"); got != ":1\r\n" {
		t.Fatalf("PEXPIRE=%q", got)
	}
	if got := execute(t, srv, "ZREMRANGEBYLEX", "z", "[b", "[c"); got != ":2\r\n" {
		t.Fatalf("remove=%q", got)
	}
	if err := journal.Close(); err != nil { t.Fatal(err) }

	restarted, err := engine.NewWithOptions(engine.Options{Shards: 16})
	if err != nil { t.Fatal(err) }
	if err := persistence.Replay(path, func(records []persistence.Record) error {
		return restarted.Restore(records, false)
	}); err != nil { t.Fatal(err) }

	items, err := restarted.ZSetRangeByLex("z", engine.ZSetLexBound{Infinite: -1}, engine.ZSetLexBound{Infinite: 1}, false, 0, -1)
	if err != nil { t.Fatal(err) }
	if len(items) != 2 || string(items[0].Member) != "a" || string(items[1].Member) != "d" {
		t.Fatalf("items=%v", items)
	}
	if ttl := restarted.TTL("z", true); ttl <= 0 || ttl > 60000 {
		t.Fatalf("ttl=%d", ttl)
	}
}
