package server

import (
	"path/filepath"
	"testing"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

func TestZSetAlgebraStoreAOFRestartRecovery(t *testing.T) {
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
	execute(t, srv, "ZADD", "left", "1", "one", "2", "two", "5", "shared")
	execute(t, srv, "ZADD", "right", "3", "one", "4", "three", "7", "shared")
	execute(t, srv, "ZADD", "dest", "99", "old")
	execute(t, srv, "PEXPIRE", "dest", "60000")

	if got := execute(t, srv, "ZUNIONSTORE", "dest", "2", "left", "right", "WEIGHTS", "2", "3"); got != ":4\r\n" {
		t.Fatalf("ZUNIONSTORE=%q", got)
	}
	if got := execute(t, srv, "ZDIFFSTORE", "diff", "2", "left", "right"); got != ":1\r\n" {
		t.Fatalf("ZDIFFSTORE=%q", got)
	}
	if got := execute(t, srv, "ZINTERSTORE", "inter", "2", "left", "right", "AGGREGATE", "MAX"); got != ":2\r\n" {
		t.Fatalf("ZINTERSTORE=%q", got)
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

	if ttl := restarted.TTL("dest", true); ttl != -1 {
		t.Fatalf("destination TTL after restart=%d want -1", ttl)
	}
	items, err := restarted.ZSetRange("dest", 0, -1, false)
	if err != nil {
		t.Fatal(err)
	}
	assertRestartZSet(t, items, []engine.ZSetItem{
		{Member: []byte("two"), Score: 4},
		{Member: []byte("one"), Score: 11},
		{Member: []byte("three"), Score: 12},
		{Member: []byte("shared"), Score: 31},
	})

	diff, err := restarted.ZSetRange("diff", 0, -1, false)
	if err != nil {
		t.Fatal(err)
	}
	assertRestartZSet(t, diff, []engine.ZSetItem{{Member: []byte("two"), Score: 2}})

	inter, err := restarted.ZSetRange("inter", 0, -1, false)
	if err != nil {
		t.Fatal(err)
	}
	assertRestartZSet(t, inter, []engine.ZSetItem{
		{Member: []byte("one"), Score: 3},
		{Member: []byte("shared"), Score: 7},
	})
}

func assertRestartZSet(t *testing.T, got, want []engine.ZSetItem) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len=%d want=%d got=%+v", len(got), len(want), got)
	}
	for i := range want {
		if string(got[i].Member) != string(want[i].Member) || got[i].Score != want[i].Score {
			t.Fatalf("item[%d]=%q/%v want=%q/%v", i, got[i].Member, got[i].Score, want[i].Member, want[i].Score)
		}
	}
}
