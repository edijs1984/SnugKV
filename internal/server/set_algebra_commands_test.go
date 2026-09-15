package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestSetAlgebraCommands(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SADD", "a", "a", "b", "d")
	execute(t, s, "SADD", "b", "b", "c", "d")

	if got := execute(t, s, "SUNION", "a", "b"); got != "*4\r\n$1\r\na\r\n$1\r\nb\r\n$1\r\nc\r\n$1\r\nd\r\n" {
		t.Fatalf("SUNION=%q", got)
	}
	if got := execute(t, s, "SINTER", "a", "b"); got != "*2\r\n$1\r\nb\r\n$1\r\nd\r\n" {
		t.Fatalf("SINTER=%q", got)
	}
	if got := execute(t, s, "SDIFF", "a", "b"); got != "*1\r\n$1\r\na\r\n" {
		t.Fatalf("SDIFF=%q", got)
	}
	if got := execute(t, s, "SUNION", "missing", "a"); got != "*3\r\n$1\r\na\r\n$1\r\nb\r\n$1\r\nd\r\n" {
		t.Fatalf("SUNION missing=%q", got)
	}
	if got := execute(t, s, "SINTER", "a", "missing"); got != "*0\r\n" {
		t.Fatalf("SINTER missing=%q", got)
	}
}

func TestSetAlgebraStoreCommands(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SADD", "a", "a", "b", "d")
	execute(t, s, "SADD", "b", "b", "c", "d")
	execute(t, s, "SET", "dest", "old")
	execute(t, s, "PEXPIRE", "dest", "60000")

	if got := execute(t, s, "SUNIONSTORE", "dest", "a", "b"); got != ":4\r\n" {
		t.Fatalf("SUNIONSTORE=%q", got)
	}
	if got := execute(t, s, "TYPE", "dest"); got != "+set\r\n" {
		t.Fatalf("TYPE dest=%q", got)
	}
	if got := execute(t, s, "PTTL", "dest"); got != ":-1\r\n" {
		t.Fatalf("SUNIONSTORE did not clear TTL: %q", got)
	}
	if got := execute(t, s, "SMEMBERS", "dest"); got != "*4\r\n$1\r\na\r\n$1\r\nb\r\n$1\r\nc\r\n$1\r\nd\r\n" {
		t.Fatalf("dest members=%q", got)
	}

	if got := execute(t, s, "SINTERSTORE", "inter", "a", "b"); got != ":2\r\n" {
		t.Fatalf("SINTERSTORE=%q", got)
	}
	if got := execute(t, s, "SDIFFSTORE", "diff", "a", "b"); got != ":1\r\n" {
		t.Fatalf("SDIFFSTORE=%q", got)
	}
	if got := execute(t, s, "SINTERSTORE", "inter", "a", "missing"); got != ":0\r\n" {
		t.Fatalf("empty SINTERSTORE=%q", got)
	}
	if got := execute(t, s, "TYPE", "inter"); got != "+none\r\n" {
		t.Fatalf("empty store did not delete destination: %q", got)
	}
}

func TestSetAlgebraStoreDestinationMayAlsoBeSource(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SADD", "a", "a", "b")
	execute(t, s, "SADD", "b", "b", "c")

	if got := execute(t, s, "SUNIONSTORE", "a", "a", "b"); got != ":3\r\n" {
		t.Fatalf("SUNIONSTORE overlapping destination=%q", got)
	}
	if got := execute(t, s, "SMEMBERS", "a"); got != "*3\r\n$1\r\na\r\n$1\r\nb\r\n$1\r\nc\r\n" {
		t.Fatalf("overlapping destination result=%q", got)
	}
}

func TestSetAlgebraWrongTypeDoesNotOverwriteDestination(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SADD", "good", "a")
	execute(t, s, "SADD", "dest", "old")
	execute(t, s, "SET", "plain", "value")

	for _, command := range [][]string{
		{"SUNION", "good", "plain"},
		{"SINTER", "missing", "plain"},
		{"SDIFF", "missing", "plain"},
		{"SUNIONSTORE", "dest", "good", "plain"},
		{"SINTERSTORE", "dest", "missing", "plain"},
		{"SDIFFSTORE", "dest", "missing", "plain"},
	} {
		args := make([][]byte, len(command))
		for i := range command {
			args[i] = []byte(command[i])
		}
		_, err := s.Execute(args)
		if err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") {
			t.Fatalf("%v err=%v", command, err)
		}
	}

	if got := execute(t, s, "SMEMBERS", "dest"); got != "*1\r\n$3\r\nold\r\n" {
		t.Fatalf("destination changed after WRONGTYPE=%q", got)
	}
}
