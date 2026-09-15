package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestListCommands(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "RPUSH", "list", "a", "b"); got != ":2\r\n" {
		t.Fatalf("RPUSH=%q", got)
	}
	if got := execute(t, s, "LPUSH", "list", "c", "d"); got != ":4\r\n" {
		t.Fatalf("LPUSH=%q", got)
	}
	if got := execute(t, s, "TYPE", "list"); got != "+list\r\n" {
		t.Fatalf("TYPE=%q", got)
	}
	if got := execute(t, s, "LLEN", "list"); got != ":4\r\n" {
		t.Fatalf("LLEN=%q", got)
	}
	if got := execute(t, s, "LRANGE", "list", "0", "-1"); got != "*4\r\n$1\r\nd\r\n$1\r\nc\r\n$1\r\na\r\n$1\r\nb\r\n" {
		t.Fatalf("LRANGE=%q", got)
	}
	if got := execute(t, s, "LINDEX", "list", "-1"); got != "$1\r\nb\r\n" {
		t.Fatalf("LINDEX=%q", got)
	}
	if got := execute(t, s, "LRANGE", "list", "1", "-2"); got != "*2\r\n$1\r\nc\r\n$1\r\na\r\n" {
		t.Fatalf("LRANGE middle=%q", got)
	}
	if got := execute(t, s, "LPOP", "list"); got != "$1\r\nd\r\n" {
		t.Fatalf("LPOP=%q", got)
	}
	if got := execute(t, s, "RPOP", "list", "2"); got != "*2\r\n$1\r\nb\r\n$1\r\na\r\n" {
		t.Fatalf("RPOP count=%q", got)
	}
	if got := execute(t, s, "LRANGE", "list", "0", "-1"); got != "*1\r\n$1\r\nc\r\n" {
		t.Fatalf("remaining=%q", got)
	}
}

func TestListMissingAndCountReplyShapes(t *testing.T) {
	s := New(engine.New())
	if got := execute(t, s, "LPOP", "missing"); got != "$-1\r\n" {
		t.Fatalf("missing LPOP=%q", got)
	}
	if got := execute(t, s, "RPOP", "missing", "2"); got != "*0\r\n" {
		t.Fatalf("missing RPOP count=%q", got)
	}
	if got := execute(t, s, "LPOP", "missing", "0"); got != "*0\r\n" {
		t.Fatalf("missing LPOP zero=%q", got)
	}
	if got := execute(t, s, "LLEN", "missing"); got != ":0\r\n" {
		t.Fatalf("missing LLEN=%q", got)
	}
	if got := execute(t, s, "LINDEX", "missing", "0"); got != "$-1\r\n" {
		t.Fatalf("missing LINDEX=%q", got)
	}
	if got := execute(t, s, "LRANGE", "missing", "0", "-1"); got != "*0\r\n" {
		t.Fatalf("missing LRANGE=%q", got)
	}
}

func TestListCommandsWrongType(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "plain", "value")
	commands := [][]string{
		{"LPUSH", "plain", "x"},
		{"RPUSH", "plain", "x"},
		{"LPOP", "plain"},
		{"LPOP", "plain", "0"},
		{"RPOP", "plain", "1"},
		{"LLEN", "plain"},
		{"LINDEX", "plain", "0"},
		{"LRANGE", "plain", "0", "-1"},
	}
	for _, command := range commands {
		args := make([][]byte, len(command))
		for i := range command {
			args[i] = []byte(command[i])
		}
		_, err := s.Execute(args)
		if err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") {
			t.Fatalf("%v err=%v", command, err)
		}
	}
}

func TestListRenamePreservesTypeTTLAndOrder(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "RPUSH", "source", "a", "b", "c")
	execute(t, s, "PEXPIRE", "source", "60000")
	if got := execute(t, s, "RENAME", "source", "target"); got != "+OK\r\n" {
		t.Fatalf("RENAME=%q", got)
	}
	if got := execute(t, s, "TYPE", "target"); got != "+list\r\n" {
		t.Fatalf("TYPE=%q", got)
	}
	if got := execute(t, s, "LRANGE", "target", "0", "-1"); got != "*3\r\n$1\r\na\r\n$1\r\nb\r\n$1\r\nc\r\n" {
		t.Fatalf("renamed LRANGE=%q", got)
	}
	pttl := execute(t, s, "PTTL", "target")
	if pttl == ":-1\r\n" || pttl == ":-2\r\n" {
		t.Fatalf("renamed LIST lost TTL: %q", pttl)
	}
}
