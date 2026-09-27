package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestJSONNumMultByBasic(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "JSON.SET", "doc", "$", "{"price":12.5,"qty":4,"name":"x"}"); got != "+OK\r\n" {
		t.Fatalf("JSON.SET=%q", got)
	}

	if got := execute(t, s, "JSON.NUMMULTBY", "doc", "$.price", "2"); got != "$2\r\n25\r\n" {
		t.Fatalf("JSON.NUMMULTBY price=%q", got)
	}
	if got := execute(t, s, "JSON.GET", "doc", "$.price"); got != "$4\r\n[25]\r\n" {
		t.Fatalf("JSON.GET price=%q", got)
	}

	if got := execute(t, s, "JSON.NUMMULTBY", "doc", "$.qty", "0.5"); got != "$1\r\n2\r\n" {
		t.Fatalf("JSON.NUMMULTBY qty=%q", got)
	}
}

func TestJSONNumMultByMissingAndNonNumeric(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "JSON.SET", "doc", "$", "{"name":"x"}")

	if got := execute(t, s, "JSON.NUMMULTBY", "missing", "$.n", "2"); got != "$-1\r\n" {
		t.Fatalf("missing key=%q", got)
	}
	if got := execute(t, s, "JSON.NUMMULTBY", "doc", "$.missing", "2"); got != "$-1\r\n" {
		t.Fatalf("missing path=%q", got)
	}
	if got := execute(t, s, "JSON.NUMMULTBY", "doc", "$.name", "2"); got != "$-1\r\n" {
		t.Fatalf("non numeric=%q", got)
	}
}

func TestJSONNumMultByErrorsAndTTL(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "JSON.SET", "doc", "$", "{"n":2}")
	execute(t, s, "PEXPIRE", "doc", "60000")

	before := execute(t, s, "PTTL", "doc")
	if before == ":-1\r\n" || before == ":-2\r\n" {
		t.Fatalf("missing TTL before JSON.NUMMULTBY: %q", before)
	}

	if got := execute(t, s, "JSON.NUMMULTBY", "doc", "$.n", "3"); got != "$1\r\n6\r\n" {
		t.Fatalf("multiply=%q", got)
	}
	if got := execute(t, s, "PTTL", "doc"); got == ":-1\r\n" || got == ":-2\r\n" {
		t.Fatalf("JSON.NUMMULTBY lost TTL: %q", got)
	}

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

	assertErrContains("not a valid number", "JSON.NUMMULTBY", "doc", "$.n", "nan")
	assertErrContains("not a valid number", "JSON.NUMMULTBY", "doc", "$.n", "inf")

	execute(t, s, "SET", "plain", "value")
	assertErrContains("WRONGTYPE", "JSON.NUMMULTBY", "plain", "$.n", "2")
}
