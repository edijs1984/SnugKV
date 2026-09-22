package server

import (
	"os"
	"path/filepath"
	"testing"

	"snugkv/internal/config"
	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

func newSearchPersistenceTestServer(t *testing.T) *TCPServer {
	t.Helper()
	cfg := config.Default()
	cfg.ListenAddr = "127.0.0.1:0"
	listener, err := ListenWithJournal(cfg, engine.New(), nil)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func executeSearchPersistence(t *testing.T, srv *Server, args ...string) []byte {
	t.Helper()
	raw := make([][]byte, len(args))
	for i := range args {
		raw[i] = []byte(args[i])
	}
	reply, err := srv.Execute(raw)
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return reply
}

func TestSearchDefinitionsPersistAndRebuildAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	snapshotPath := filepath.Join(dir, "snapshot.db")

	first := newSearchPersistenceTestServer(t)
	if err := first.ConfigureSearchPersistence("", snapshotPath); err != nil {
		t.Fatalf("configure first persistence: %v", err)
	}

	executeSearchPersistence(t, first.server,
		"JSON.SET", "product:1", "$",
		`{"category":"books","price":12}`,
	)
	executeSearchPersistence(t, first.server,
		"FT.CREATE", "products", "ON", "JSON",
		"PREFIX", "1", "product:",
		"SCHEMA",
		"$.category", "AS", "category", "TAG",
		"$.price", "AS", "price", "NUMERIC",
	)

	statePath := snapshotPath + ".search"
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("search state file: %v", err)
	}

	records := first.server.store.Export(nil)
	if err := first.Close(); err != nil {
		t.Fatalf("close first server: %v", err)
	}

	second := newSearchPersistenceTestServer(t)
	if err := second.server.store.Restore(records, false); err != nil {
		t.Fatalf("restore primary data: %v", err)
	}
	if err := second.ConfigureSearchPersistence("", snapshotPath); err != nil {
		t.Fatalf("configure second persistence: %v", err)
	}

	keys, ok := second.server.store.SearchTagKeys("products", "category", "books")
	if !ok || len(keys) != 1 || keys[0] != "product:1" {
		t.Fatalf("rebuilt tag index keys=%v ok=%v", keys, ok)
	}
	keys, ok = second.server.store.SearchNumericRangeKeys("products", "price", 10, 20)
	if !ok || len(keys) != 1 || keys[0] != "product:1" {
		t.Fatalf("rebuilt numeric index keys=%v ok=%v", keys, ok)
	}

	if got := executeSearchPersistence(t, second.server,
		"FT.SEARCH", "products", "@category:{books}", "NOCONTENT",
	); string(got) != "*2\r\n:1\r\n$9\r\nproduct:1\r\n" {
		t.Fatalf("FT.SEARCH after restart = %q", got)
	}
}

func TestSearchDropPersistsEmptyRegistry(t *testing.T) {
	dir := t.TempDir()
	aofPath := filepath.Join(dir, "appendonly.aof")

	first := newSearchPersistenceTestServer(t)
	if err := first.ConfigureSearchPersistence(aofPath, ""); err != nil {
		t.Fatalf("configure persistence: %v", err)
	}

	executeSearchPersistence(t, first.server,
		"FT.CREATE", "products", "ON", "JSON",
		"SCHEMA", "$.category", "AS", "category", "TAG",
	)
	executeSearchPersistence(t, first.server, "FT.DROPINDEX", "products")

	second := newSearchPersistenceTestServer(t)
	if err := second.ConfigureSearchPersistence(aofPath, ""); err != nil {
		t.Fatalf("restore persistence: %v", err)
	}
	if got := executeSearchPersistence(t, second.server, "FT._LIST"); string(got) != "*0\r\n" {
		t.Fatalf("FT._LIST after persisted drop = %q", got)
	}
}

func TestSearchRecoveryRejectsCorruptState(t *testing.T) {
	dir := t.TempDir()
	snapshotPath := filepath.Join(dir, "snapshot.db")
	if err := os.WriteFile(snapshotPath+".search", []byte("{bad"), 0600); err != nil {
		t.Fatal(err)
	}

	listener := newSearchPersistenceTestServer(t)
	if err := listener.ConfigureSearchPersistence("", snapshotPath); err == nil {
		t.Fatal("corrupt search state unexpectedly accepted")
	}
}

type searchCountingJournal struct {
	appends int
	records int
}

func (j *searchCountingJournal) Append(records []persistence.Record) error {
	j.appends++
	j.records += len(records)
	return nil
}

func TestSearchDefinitionMutationsDoNotJournalKeyspace(t *testing.T) {
	store := engine.New()
	applied, err := store.JSONSet(
		"product:1",
		"$",
		[]byte(`{"category":"books"}`),
		false,
		false,
	)
	if err != nil || !applied {
		t.Fatalf("seed JSON: applied=%v err=%v", applied, err)
	}

	journal := &searchCountingJournal{}
	srv := New(store)
	srv.SetJournal(journal)

	executeSearchPersistence(t, srv,
		"FT.CREATE", "products", "ON", "JSON",
		"SCHEMA", "$.category", "AS", "category", "TAG",
	)
	executeSearchPersistence(t, srv, "FT.DROPINDEX", "products")

	if journal.appends != 0 || journal.records != 0 {
		t.Fatalf("search metadata reached keyspace journal: appends=%d records=%d", journal.appends, journal.records)
	}
}
