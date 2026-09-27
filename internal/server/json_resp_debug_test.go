package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestJSONRESPBasicMapping(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "JSON.SET", "doc", "$", "{\"a\":1,\"b\":[true,null,\"x\"],\"c\":1.5}")

	got := execute(t, s, "JSON.RESP", "doc")
	want := "*7\r\n+{\r\n$1\r\na\r\n:1\r\n$1\r\nb\r\n*4\r\n+[\r\n+true\r\n$-1\r\n$1\r\nx\r\n$1\r\nc\r\n$3\r\n1.5\r\n"
	if got != want {
		t.Fatalf("JSON.RESP root=%q want=%q", got, want)
	}
}

func TestJSONRESPPathAndMissing(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "JSON.SET", "doc", "$", "{\"a\":1,\"nested\":{\"a\":2}}")

	if got := execute(t, s, "JSON.RESP", "doc", "$..a"); got != "*2\r\n:1\r\n:2\r\n" {
		t.Fatalf("JSON.RESP recursive path=%q", got)
	}
	if got := execute(t, s, "JSON.RESP", "doc", ".nested.a"); got != ":2\r\n" {
		t.Fatalf("JSON.RESP legacy path=%q", got)
	}
	if got := execute(t, s, "JSON.RESP", "missing"); got != "$-1\r\n" {
		t.Fatalf("JSON.RESP missing=%q", got)
	}
}

func TestJSONDebugMemoryAndHelp(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "JSON.SET", "doc", "$", "{\"a\":1,\"b\":[2,3],\"s\":\"hello\"}")

	root := execute(t, s, "JSON.DEBUG", "MEMORY", "doc")
	if !strings.HasPrefix(root, ":") || root == ":0\r\n" {
		t.Fatalf("JSON.DEBUG MEMORY root=%q", root)
	}

	path := execute(t, s, "JSON.DEBUG", "MEMORY", "doc", "$.b[*]")
	if path != "*2\r\n:8\r\n:8\r\n" {
		t.Fatalf("JSON.DEBUG MEMORY path=%q", path)
	}

	help := execute(t, s, "JSON.DEBUG", "HELP")
	if !strings.Contains(help, "MEMORY") || !strings.Contains(help, "HELP") {
		t.Fatalf("JSON.DEBUG HELP=%q", help)
	}

	if got := execute(t, s, "JSON.DEBUG", "MEMORY", "missing"); got != "$-1\r\n" {
		t.Fatalf("JSON.DEBUG MEMORY missing=%q", got)
	}
}

func TestJSONRespDebugWrongTypeAndErrors(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "plain", "value")

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

	assertErrContains("WRONGTYPE", "JSON.RESP", "plain")
	assertErrContains("WRONGTYPE", "JSON.DEBUG", "MEMORY", "plain")
	assertErrContains("unknown subcommand", "JSON.DEBUG", "NOPE")
}
