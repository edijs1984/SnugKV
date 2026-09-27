package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestSearchAggregateCursorPaging(t *testing.T) {
	s := New(engine.New())

	execute(t, s,
		"FT.CREATE", "idx", "ON", "JSON",
		"PREFIX", "1", "doc:",
		"SCHEMA", "$.kind", "AS", "kind", "TAG",
	)
	for i := 1; i <= 5; i++ {
		execute(t, s, "JSON.SET",
			"doc:"+string(rune('0'+i)),
			"$",
			"{\"kind\":\"x\"}",
		)
	}

	got := execute(t, s,
		"FT.AGGREGATE", "idx", "*",
		"LOAD", "1", "@kind",
		"WITHCURSOR", "COUNT", "2",
	)
	if !strings.HasPrefix(got, "*2\r\n") {
		t.Fatalf("WITHCURSOR outer shape=%q", got)
	}
	if !strings.Contains(got, ":5\r\n") {
		t.Fatalf("WITHCURSOR missing total=%q", got)
	}

	// Cursor id is the final integer in the outer reply. The first created
	// cursor in a new server is id 1.
	if !strings.HasSuffix(got, ":1\r\n") {
		t.Fatalf("WITHCURSOR expected cursor id 1: %q", got)
	}

	got = execute(t, s, "FT.CURSOR", "READ", "idx", "1", "COUNT", "2")
	if !strings.HasPrefix(got, "*2\r\n") || !strings.HasSuffix(got, ":1\r\n") {
		t.Fatalf("cursor second page=%q", got)
	}

	got = execute(t, s, "FT.CURSOR", "READ", "idx", "1", "COUNT", "10")
	if !strings.HasSuffix(got, ":0\r\n") {
		t.Fatalf("cursor exhaustion did not return 0: %q", got)
	}

	raw := [][]byte{[]byte("FT.CURSOR"), []byte("READ"), []byte("idx"), []byte("1")}
	if _, err := s.Execute(raw); err == nil || !strings.Contains(err.Error(), "Cursor not found") {
		t.Fatalf("exhausted cursor error=%v", err)
	}
}

func TestSearchCursorDelete(t *testing.T) {
	s := New(engine.New())
	execute(t, s,
		"FT.CREATE", "idx", "ON", "JSON",
		"SCHEMA", "$.kind", "AS", "kind", "TAG",
	)
	for i := 0; i < 3; i++ {
		execute(t, s, "JSON.SET", "doc:"+string(rune('a'+i)), "$", "{\"kind\":\"x\"}")
	}
	execute(t, s,
		"FT.AGGREGATE", "idx", "*",
		"LOAD", "1", "@kind",
		"WITHCURSOR", "COUNT", "1",
	)

	if got := execute(t, s, "FT.CURSOR", "DEL", "idx", "1"); got != "+OK\r\n" {
		t.Fatalf("FT.CURSOR DEL=%q", got)
	}
	raw := [][]byte{[]byte("FT.CURSOR"), []byte("READ"), []byte("idx"), []byte("1")}
	if _, err := s.Execute(raw); err == nil || !strings.Contains(err.Error(), "Cursor not found") {
		t.Fatalf("deleted cursor error=%v", err)
	}
}

func TestSearchConfigGetSet(t *testing.T) {
	s := New(engine.New())

	got := execute(t, s, "FT.CONFIG", "GET", "DEFAULT_DIALECT")
	if !strings.Contains(got, "DEFAULT_DIALECT") || !strings.Contains(got, "$1\r\n1\r\n") {
		t.Fatalf("initial FT.CONFIG GET=%q", got)
	}

	if got := execute(t, s, "FT.CONFIG", "SET", "DEFAULT_DIALECT", "2"); got != "+OK\r\n" {
		t.Fatalf("FT.CONFIG SET=%q", got)
	}
	got = execute(t, s, "FT.CONFIG", "GET", "DEFAULT_DIALECT")
	if !strings.Contains(got, "$1\r\n2\r\n") {
		t.Fatalf("updated FT.CONFIG GET=%q", got)
	}

	got = execute(t, s, "FT.CONFIG", "GET", "*")
	for _, name := range []string{"DEFAULT_DIALECT", "MAXSEARCHRESULTS", "MAXAGGREGATERESULTS", "CURSOR_MAX_IDLE"} {
		if !strings.Contains(got, name) {
			t.Fatalf("FT.CONFIG GET * missing %q: %q", name, got)
		}
	}
}

func TestSearchCursorConfigErrors(t *testing.T) {
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

	assertErrContains("invalid config value", "FT.CONFIG", "SET", "DEFAULT_DIALECT", "9")
	assertErrContains("Unsupported CONFIG", "FT.CONFIG", "SET", "UNKNOWN", "1")
	assertErrContains("invalid cursor", "FT.CURSOR", "READ", "idx", "abc")
}
