package server

import (
	"path/filepath"
	"testing"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

func TestHashAOFRestartRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.snug")

	first, err := engine.NewWithOptions(engine.Options{
		Shards:        16,
		Encoding:      true,
		ShapeEncoding: true,
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

	if got := execute(t, srv, "HMSET", "hash", "count", "1", "score", "1.5", "remove", "x"); got != "+OK\r\n" {
		t.Fatalf("HMSET = %q", got)
	}
	if got := execute(t, srv, "PEXPIRE", "hash", "60000"); got != ":1\r\n" {
		t.Fatalf("PEXPIRE = %q", got)
	}
	if got := execute(t, srv, "HINCRBY", "hash", "count", "4"); got != ":5\r\n" {
		t.Fatalf("HINCRBY = %q", got)
	}
	if got := execute(t, srv, "HINCRBYFLOAT", "hash", "score", "0.5"); got != "$1\r\n2\r\n" {
		t.Fatalf("HINCRBYFLOAT = %q", got)
	}
	if got := execute(t, srv, "HDEL", "hash", "remove"); got != ":1\r\n" {
		t.Fatalf("HDEL = %q", got)
	}

	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := engine.NewWithOptions(engine.Options{
		Shards:        16,
		Encoding:      true,
		ShapeEncoding: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.Replay(path, func(records []persistence.Record) error {
		return restarted.Restore(records, false)
	}); err != nil {
		t.Fatal(err)
	}

	if value, found, err := restarted.HashGet("hash", []byte("count")); err != nil || !found || string(value) != "5" {
		t.Fatalf("count after restart = %q %v %v", value, found, err)
	}
	if value, found, err := restarted.HashGet("hash", []byte("score")); err != nil || !found || string(value) != "2" {
		t.Fatalf("score after restart = %q %v %v", value, found, err)
	}
	if _, found, err := restarted.HashGet("hash", []byte("remove")); err != nil || found {
		t.Fatalf("removed field after restart found=%v err=%v", found, err)
	}

	ttl := restarted.TTL("hash", true)
	if ttl <= 0 || ttl > 60000 {
		t.Fatalf("HASH TTL after restart = %d", ttl)
	}
}
