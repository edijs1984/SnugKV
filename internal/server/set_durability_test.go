package server

import (
	"path/filepath"
	"testing"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

func TestSetAOFRestartRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.snug")

	first, err := engine.NewWithOptions(engine.Options{
		Shards:   16,
		Encoding: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := persistence.Open(path, "always")
	if err != nil {
		t.Fatal(err)
	}

	srv := New(first)
	srv.SetJournal(journal)
	if got := execute(t, srv, "SADD", "set", "a", "b", "remove"); got != ":3\r\n" {
		t.Fatalf("SADD=%q", got)
	}
	if got := execute(t, srv, "PEXPIRE", "set", "60000"); got != ":1\r\n" {
		t.Fatalf("PEXPIRE=%q", got)
	}
	if got := execute(t, srv, "SREM", "set", "remove"); got != ":1\r\n" {
		t.Fatalf("SREM=%q", got)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := engine.NewWithOptions(engine.Options{
		Shards:   16,
		Encoding: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.Replay(path, func(records []persistence.Record) error {
		return restarted.Restore(records, false)
	}); err != nil {
		t.Fatal(err)
	}

	if got := restarted.Type("set"); got != "set" {
		t.Fatalf("type after restart=%q", got)
	}
	for _, member := range []string{"a", "b"} {
		found, err := restarted.SetContains("set", []byte(member))
		if err != nil || !found {
			t.Fatalf("member %q after restart found=%v err=%v", member, found, err)
		}
	}
	found, err := restarted.SetContains("set", []byte("remove"))
	if err != nil || found {
		t.Fatalf("removed member after restart found=%v err=%v", found, err)
	}
	ttl := restarted.TTL("set", true)
	if ttl <= 0 || ttl > 60000 {
		t.Fatalf("SET TTL after restart=%d", ttl)
	}
}
