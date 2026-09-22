package server

import (
	"path/filepath"
	"testing"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

func TestJSONPathAOFRestartRecovery(t *testing.T) {
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

	if got := execute(t, srv, "JSON.SET", "doc", "$",
		`{"items":[{"name":"a","score":1,"active":false},{"name":"b","score":2,"active":false},{"name":"c","score":3,"active":false}]}`); got != "+OK\r\n" {
		t.Fatalf("JSON.SET root=%q", got)
	}
	if got := execute(t, srv, "JSON.SET", "doc", "$.items[?(@.score >= 2)].active", "true"); got != "+OK\r\n" {
		t.Fatalf("JSON.SET filtered=%q", got)
	}
	if got := execute(t, srv, "JSON.DEL", "doc", "$.items[?(@.score == 1)]"); got != ":1\r\n" {
		t.Fatalf("JSON.DEL filtered=%q", got)
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

	restartedSrv := New(restarted)

	if got := execute(t, restartedSrv, "JSON.GET", "doc", "$.items[*].name"); got != "$9\r\n[\"b\",\"c\"]\r\n" {
		t.Fatalf("names after restart=%q", got)
	}
	if got := execute(t, restartedSrv, "JSON.GET", "doc", "$.items[?(@.active == true)].score"); got != "$5\r\n[2,3]\r\n" {
		t.Fatalf("filtered scores after restart=%q", got)
	}
	if got := execute(t, restartedSrv, "TYPE", "doc"); got != "+string\r\n" {
		t.Fatalf("redis type after restart=%q", got)
	}
	if got := execute(t, restartedSrv, "SNUG.TYPE", "doc"); got != "$4\r\nJSON\r\n" {
		t.Fatalf("semantic type after restart=%q", got)
	}
}
