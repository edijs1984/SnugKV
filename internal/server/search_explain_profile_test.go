package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestSearchExplainAndCLI(t *testing.T) {
	s := New(engine.New())

	execute(t, s,
		"FT.CREATE", "idx", "ON", "JSON",
		"SCHEMA",
		"$.title", "AS", "title", "TEXT",
		"$.kind", "AS", "kind", "TAG",
	)

	got := execute(t, s, "FT.EXPLAIN", "idx", "@title:(hello world)")
	for _, want := range []string{"INTERSECT {", "@title:TEXT{hello}", "@title:TEXT{world}"} {
		if !strings.Contains(got, want) {
			t.Fatalf("FT.EXPLAIN missing %q: %q", want, got)
		}
	}

	got = execute(t, s, "FT.EXPLAINCLI", "idx", "-@kind:{archived}")
	if !strings.HasPrefix(got, "*") || !strings.Contains(got, "NOT {") || !strings.Contains(got, "@kind:TAG{archived}") {
		t.Fatalf("FT.EXPLAINCLI=%q", got)
	}
}

func TestSearchExplainDialect(t *testing.T) {
	s := New(engine.New())
	execute(t, s,
		"FT.CREATE", "idx", "ON", "JSON",
		"SCHEMA", "$.title", "AS", "title", "TEXT",
	)

	got := execute(t, s, "FT.EXPLAIN", "idx", "@title:hello", "DIALECT", "2")
	if !strings.Contains(got, "@title:TEXT{hello}") {
		t.Fatalf("FT.EXPLAIN DIALECT=%q", got)
	}

	raw := [][]byte{
		[]byte("FT.EXPLAIN"), []byte("idx"), []byte("@title:hello"),
		[]byte("DIALECT"), []byte("9"),
	}
	if _, err := s.Execute(raw); err == nil || !strings.Contains(err.Error(), "unsupported search dialect") {
		t.Fatalf("invalid dialect err=%v", err)
	}
}

func TestSearchProfileSearch(t *testing.T) {
	s := New(engine.New())
	execute(t, s,
		"FT.CREATE", "idx", "ON", "JSON",
		"SCHEMA", "$.title", "AS", "title", "TEXT",
	)
	execute(t, s, "JSON.SET", "doc:1", "$", "{\"title\":\"hello world\"}")

	got := execute(t, s,
		"FT.PROFILE", "idx", "SEARCH", "QUERY",
		"@title:hello", "NOCONTENT",
	)
	if !strings.HasPrefix(got, "*2\r\n") {
		t.Fatalf("FT.PROFILE SEARCH outer shape=%q", got)
	}
	for _, want := range []string{"doc:1", "Total profile time", "Result processors profile", "Iterators profile", "SEARCH"} {
		if !strings.Contains(got, want) {
			t.Fatalf("FT.PROFILE SEARCH missing %q: %q", want, got)
		}
	}
}

func TestSearchProfileAggregateLimited(t *testing.T) {
	s := New(engine.New())
	execute(t, s,
		"FT.CREATE", "idx", "ON", "JSON",
		"SCHEMA", "$.kind", "AS", "kind", "TAG",
	)
	execute(t, s, "JSON.SET", "doc:1", "$", "{\"kind\":\"a\"}")
	execute(t, s, "JSON.SET", "doc:2", "$", "{\"kind\":\"b\"}")

	got := execute(t, s,
		"FT.PROFILE", "idx", "AGGREGATE", "LIMITED", "QUERY",
		"*", "LOAD", "1", "@kind",
	)
	if !strings.HasPrefix(got, "*2\r\n") {
		t.Fatalf("FT.PROFILE AGGREGATE outer shape=%q", got)
	}
	if !strings.Contains(got, "kind") || !strings.Contains(got, "AGGREGATE") {
		t.Fatalf("FT.PROFILE AGGREGATE=%q", got)
	}
	if strings.Contains(got, "Iterators profile") {
		t.Fatalf("LIMITED profile unexpectedly contains iterator details: %q", got)
	}
}

func TestSearchExplainProfileErrors(t *testing.T) {
	s := New(engine.New())

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

	assertErrContains("Index not found", "FT.EXPLAIN", "missing", "*")
	assertErrContains("Index not found", "FT.PROFILE", "missing", "SEARCH", "QUERY", "*")

	execute(t, s,
		"FT.CREATE", "idx", "ON", "JSON",
		"SCHEMA", "$.title", "AS", "title", "TEXT",
	)
	assertErrContains("syntax error", "FT.PROFILE", "idx", "BAD", "QUERY", "*")
	assertErrContains("syntax error", "FT.PROFILE", "idx", "SEARCH", "BAD", "*")
}
