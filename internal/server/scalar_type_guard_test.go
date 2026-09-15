package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func seedNativeScalarGuardKeys(t *testing.T, s *Server) []string {
	t.Helper()
	if got := execute(t, s, "HSET", "guard:h", "f", "v"); got != ":1\r\n" {
		t.Fatalf("HSET=%q", got)
	}
	if got := execute(t, s, "SADD", "guard:s", "v"); got != ":1\r\n" {
		t.Fatalf("SADD=%q", got)
	}
	if got := execute(t, s, "RPUSH", "guard:l", "v"); got != ":1\r\n" {
		t.Fatalf("RPUSH=%q", got)
	}
	if got := execute(t, s, "ZADD", "guard:z", "1", "v"); got != ":1\r\n" {
		t.Fatalf("ZADD=%q", got)
	}
	return []string{"guard:h", "guard:s", "guard:l", "guard:z"}
}

func executeExpectWrongType(t *testing.T, s *Server, args ...string) {
	t.Helper()
	command := make([][]byte, len(args))
	for i := range args {
		command[i] = []byte(args[i])
	}
	_, err := s.Execute(command)
	if err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE ") {
		t.Fatalf("%v err=%v, want WRONGTYPE", args, err)
	}
}

func TestLegacyScalarCommandsRejectNativeContainers(t *testing.T) {
	s := New(engine.New())
	keys := seedNativeScalarGuardKeys(t, s)

	for _, key := range keys {
		commands := [][]string{
			{"GET", key},
			{"GETSET", key, "x"},
			{"GETEX", key},
			{"APPEND", key, "x"},
			{"GETRANGE", key, "0", "-1"},
			{"SETRANGE", key, "0", "x"},
			{"STRLEN", key},
			{"INCR", key},
			{"DECR", key},
			{"INCRBY", key, "2"},
			{"DECRBY", key, "2"},
			{"INCRBYFLOAT", key, "1.5"},
			{"GETBIT", key, "0"},
			{"SETBIT", key, "0", "1"},
			{"BITCOUNT", key},
			{"BITPOS", key, "1"},
		}
		for _, command := range commands {
			executeExpectWrongType(t, s, command...)
		}
	}
}

func TestSetGetRejectsNativeContainerAndDoesNotOverwrite(t *testing.T) {
	s := New(engine.New())
	seedNativeScalarGuardKeys(t, s)

	executeExpectWrongType(t, s, "SET", "guard:h", "replacement", "GET")
	if got := execute(t, s, "TYPE", "guard:h"); got != "+hash\r\n" {
		t.Fatalf("TYPE after SET GET=%q", got)
	}
	if got := execute(t, s, "HGET", "guard:h", "f"); got != "$1\r\nv\r\n" {
		t.Fatalf("HGET after SET GET=%q", got)
	}

	// Plain SET intentionally overwrites any Redis type.
	if got := execute(t, s, "SET", "guard:h", "replacement"); got != "+OK\r\n" {
		t.Fatalf("plain SET=%q", got)
	}
	if got := execute(t, s, "GET", "guard:h"); got != "$11\r\nreplacement\r\n" {
		t.Fatalf("GET replacement=%q", got)
	}
}

func TestMGetReturnsNilForNativeContainers(t *testing.T) {
	s := New(engine.New())
	seedNativeScalarGuardKeys(t, s)
	execute(t, s, "SET", "guard:string", "ok")

	got := execute(t, s, "MGET", "guard:string", "guard:h", "guard:s", "guard:l", "guard:z", "missing")
	want := "*6\r\n$2\r\nok\r\n$-1\r\n$-1\r\n$-1\r\n$-1\r\n$-1\r\n"
	if got != want {
		t.Fatalf("MGET=%q want %q", got, want)
	}
}

func TestGetDelReturnsNilAndPreservesNativeContainers(t *testing.T) {
	s := New(engine.New())
	keys := seedNativeScalarGuardKeys(t, s)

	for _, key := range keys {
		if got := execute(t, s, "GETDEL", key); got != "$-1\r\n" {
			t.Fatalf("GETDEL %s=%q", key, got)
		}
		if got := execute(t, s, "EXISTS", key); got != ":1\r\n" {
			t.Fatalf("EXISTS %s after GETDEL=%q", key, got)
		}
	}

	execute(t, s, "SET", "guard:string", "value")
	if got := execute(t, s, "GETDEL", "guard:string"); got != "$5\r\nvalue\r\n" {
		t.Fatalf("GETDEL string=%q", got)
	}
	if got := execute(t, s, "EXISTS", "guard:string"); got != ":0\r\n" {
		t.Fatalf("string still exists after GETDEL: %q", got)
	}
}

func TestBitOpChecksSourcesButMayOverwriteDestinationType(t *testing.T) {
	s := New(engine.New())
	seedNativeScalarGuardKeys(t, s)
	execute(t, s, "SET", "guard:bits", "A")

	executeExpectWrongType(t, s, "BITOP", "OR", "guard:dest", "guard:bits", "guard:s")

	// Redis permits BITOP to replace a destination regardless of its old type.
	if got := execute(t, s, "HSET", "guard:dest", "f", "v"); got != ":1\r\n" {
		t.Fatalf("HSET dest=%q", got)
	}
	if got := execute(t, s, "BITOP", "NOT", "guard:dest", "guard:bits"); got != ":1\r\n" {
		t.Fatalf("BITOP overwrite=%q", got)
	}
	if got := execute(t, s, "TYPE", "guard:dest"); got != "+string\r\n" {
		t.Fatalf("TYPE destination=%q", got)
	}
}

func TestWrongTypeTCPPrefixIsPreserved(t *testing.T) {
	got := string(errorResponse(errWrongType))
	want := "-WRONGTYPE Operation against a key holding the wrong kind of value\r\n"
	if got != want {
		t.Fatalf("errorResponse=%q want %q", got, want)
	}
}
