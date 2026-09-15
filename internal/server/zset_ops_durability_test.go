package server

import (
	"path/filepath"
	"testing"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

func TestZSetOpsAOFRestartRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.snug")
	first, err := engine.NewWithOptions(engine.Options{Shards: 16})
	if err != nil { t.Fatal(err) }
	journal, err := persistence.Open(path, "always")
	if err != nil { t.Fatal(err) }
	srv := New(first)
	srv.SetJournal(journal)
	execute(t, srv, "ZADD", "a", "1", "one", "2", "two", "3", "three")
	execute(t, srv, "ZADD", "b", "10", "x", "20", "y")
	if got := execute(t, srv, "ZPOPMIN", "a"); got != "*2\r\n$3\r\none\r\n$1\r\n1\r\n" { t.Fatalf("pop=%q", got) }
	if got := execute(t, srv, "ZMPOP", "2", "missing", "b", "MAX"); got != "*2\r\n$1\r\nb\r\n*1\r\n*2\r\n$1\r\ny\r\n$2\r\n20\r\n" { t.Fatalf("zmpop=%q", got) }
	if got := execute(t, srv, "ZRANGESTORE", "copy", "a", "0", "-1"); got != ":2\r\n" { t.Fatalf("store=%q", got) }
	if err := journal.Close(); err != nil { t.Fatal(err) }

	restarted, err := engine.NewWithOptions(engine.Options{Shards: 16})
	if err != nil { t.Fatal(err) }
	if err := persistence.Replay(path, func(records []persistence.Record) error { return restarted.Restore(records, false) }); err != nil { t.Fatal(err) }
	items, err := restarted.ZSetRange("a", 0, -1, false)
	if err != nil || len(items) != 2 || string(items[0].Member) != "two" || string(items[1].Member) != "three" { t.Fatalf("a=%v err=%v", items, err) }
	items, err = restarted.ZSetRange("b", 0, -1, false)
	if err != nil || len(items) != 1 || string(items[0].Member) != "x" { t.Fatalf("b=%v err=%v", items, err) }
	items, err = restarted.ZSetRange("copy", 0, -1, false)
	if err != nil || len(items) != 2 || string(items[0].Member) != "two" || string(items[1].Member) != "three" { t.Fatalf("copy=%v err=%v", items, err) }
}
