package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestSearchAliases(t *testing.T) {
	s := New(engine.New())

	execute(t, s,
		"FT.CREATE", "idx1", "ON", "JSON",
		"PREFIX", "1", "doc:",
		"SCHEMA", "$.kind", "AS", "kind", "TAG",
	)
	execute(t, s,
		"FT.CREATE", "idx2", "ON", "JSON",
		"PREFIX", "1", "other:",
		"SCHEMA", "$.kind", "AS", "kind", "TAG",
	)
	execute(t, s, "JSON.SET", "doc:1", "$", "{\"kind\":\"one\"}")
	execute(t, s, "JSON.SET", "other:1", "$", "{\"kind\":\"two\"}")

	if got := execute(t, s, "FT.ALIASADD", "current", "idx1"); got != "+OK\r\n" {
		t.Fatalf("FT.ALIASADD=%q", got)
	}

	got := execute(t, s, "FT.SEARCH", "current", "@kind:{one}", "NOCONTENT")
	if !strings.Contains(got, "doc:1") {
		t.Fatalf("search via alias=%q", got)
	}
	if got := execute(t, s, "FT.INFO", "current"); !strings.Contains(got, "idx1") {
		t.Fatalf("FT.INFO alias did not resolve target: %q", got)
	}

	if got := execute(t, s, "FT.ALIASUPDATE", "current", "idx2"); got != "+OK\r\n" {
		t.Fatalf("FT.ALIASUPDATE=%q", got)
	}
	got = execute(t, s, "FT.SEARCH", "current", "@kind:{two}", "NOCONTENT")
	if !strings.Contains(got, "other:1") || strings.Contains(got, "doc:1") {
		t.Fatalf("updated alias search=%q", got)
	}

	if got := execute(t, s, "FT.ALIASDEL", "current"); got != "+OK\r\n" {
		t.Fatalf("FT.ALIASDEL=%q", got)
	}
	raw := [][]byte{[]byte("FT.SEARCH"), []byte("current"), []byte("*")}
	if _, err := s.Execute(raw); err == nil || !strings.Contains(err.Error(), "Index not found") {
		t.Fatalf("deleted alias error=%v", err)
	}
}

func TestSearchTagVals(t *testing.T) {
	s := New(engine.New())
	execute(t, s,
		"FT.CREATE", "idx", "ON", "JSON",
		"SCHEMA", "$.tag", "AS", "tag", "TAG",
	)
	execute(t, s, "JSON.SET", "doc:1", "$", "{\"tag\":\"beta\"}")
	execute(t, s, "JSON.SET", "doc:2", "$", "{\"tag\":\"alpha\"}")
	execute(t, s, "JSON.SET", "doc:3", "$", "{\"tag\":\"beta\"}")

	got := execute(t, s, "FT.TAGVALS", "idx", "tag")
	if !strings.HasPrefix(got, "*2\r\n") {
		t.Fatalf("FT.TAGVALS count=%q", got)
	}
	if strings.Index(got, "alpha") > strings.Index(got, "beta") {
		t.Fatalf("FT.TAGVALS not sorted: %q", got)
	}

	execute(t, s, "FT.ALIASADD", "alias", "idx")
	got = execute(t, s, "FT.TAGVALS", "alias", "tag")
	if !strings.Contains(got, "alpha") || !strings.Contains(got, "beta") {
		t.Fatalf("FT.TAGVALS alias=%q", got)
	}
}

func TestSearchAlterBackfills(t *testing.T) {
	s := New(engine.New())
	execute(t, s,
		"FT.CREATE", "idx", "ON", "JSON",
		"SCHEMA", "$.title", "AS", "title", "TEXT",
	)
	execute(t, s, "JSON.SET", "doc:1", "$", "{\"title\":\"hello\",\"category\":\"news\"}")

	if got := execute(t, s,
		"FT.ALTER", "idx", "SCHEMA", "ADD",
		"$.category", "AS", "category", "TAG",
	); got != "+OK\r\n" {
		t.Fatalf("FT.ALTER=%q", got)
	}

	got := execute(t, s, "FT.SEARCH", "idx", "@category:{news}", "NOCONTENT")
	if !strings.Contains(got, "doc:1") {
		t.Fatalf("altered field did not backfill: %q", got)
	}
	got = execute(t, s, "FT.INFO", "idx")
	if !strings.Contains(got, "category") || !strings.Contains(got, "TAG") {
		t.Fatalf("FT.INFO missing altered field: %q", got)
	}
}

func TestSearchAlterSkipInitialScan(t *testing.T) {
	s := New(engine.New())
	execute(t, s,
		"FT.CREATE", "idx", "ON", "JSON",
		"SCHEMA", "$.title", "AS", "title", "TEXT",
	)
	execute(t, s, "JSON.SET", "doc:1", "$", "{\"title\":\"hello\",\"category\":\"news\"}")

	execute(t, s,
		"FT.ALTER", "idx", "SKIPINITIALSCAN", "SCHEMA", "ADD",
		"$.category", "AS", "category", "TAG",
	)

	if got := execute(t, s, "FT.SEARCH", "idx", "@category:{news}", "NOCONTENT"); strings.Contains(got, "doc:1") {
		t.Fatalf("SKIPINITIALSCAN unexpectedly backfilled: %q", got)
	}

	execute(t, s, "JSON.SET", "doc:1", "$", "{\"title\":\"hello again\",\"category\":\"news\"}")
	if got := execute(t, s, "FT.SEARCH", "idx", "@category:{news}", "NOCONTENT"); !strings.Contains(got, "doc:1") {
		t.Fatalf("new field not indexed after mutation: %q", got)
	}
}

func TestSearchAliasAndAlterErrors(t *testing.T) {
	s := New(engine.New())
	execute(t, s,
		"FT.CREATE", "idx", "ON", "JSON",
		"SCHEMA", "$.tag", "AS", "tag", "TAG",
	)

	assertErrContains := func(want string, args ...string) {
		t.Helper()
		raw := make([][]byte, len(args))
		for i := range args {
			raw[i] = []byte(args[i])
		}
		_, err := s.Execute(raw)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q error=%v want substring %q", args, err, want)
		}
	}

	assertErrContains("Index not found", "FT.ALIASADD", "a", "missing")
	execute(t, s, "FT.ALIASADD", "a", "idx")
	assertErrContains("already exists", "FT.ALIASADD", "a", "idx")
	assertErrContains("Duplicate field", "FT.ALTER", "idx", "SCHEMA", "ADD", "$.other", "AS", "tag", "TAG")
	assertErrContains("not a TAG field", "FT.TAGVALS", "idx", "missing")
}
