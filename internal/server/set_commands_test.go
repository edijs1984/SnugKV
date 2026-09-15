package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestSetCommands(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "SADD", "letters", "b", "a", "b", "c"); got != ":3\r\n" {
		t.Fatalf("SADD=%q", got)
	}
	if got := execute(t, s, "TYPE", "letters"); got != "+set\r\n" {
		t.Fatalf("TYPE=%q", got)
	}
	if got := execute(t, s, "SCARD", "letters"); got != ":3\r\n" {
		t.Fatalf("SCARD=%q", got)
	}
	if got := execute(t, s, "SISMEMBER", "letters", "a"); got != ":1\r\n" {
		t.Fatalf("SISMEMBER existing=%q", got)
	}
	if got := execute(t, s, "SISMEMBER", "letters", "z"); got != ":0\r\n" {
		t.Fatalf("SISMEMBER missing=%q", got)
	}
	if got := execute(t, s, "SMEMBERS", "letters"); got != "*3\r\n$1\r\na\r\n$1\r\nb\r\n$1\r\nc\r\n" {
		t.Fatalf("SMEMBERS=%q", got)
	}
	if got := execute(t, s, "SREM", "letters", "a", "missing"); got != ":1\r\n" {
		t.Fatalf("SREM=%q", got)
	}
	if got := execute(t, s, "SCARD", "missing"); got != ":0\r\n" {
		t.Fatalf("missing SCARD=%q", got)
	}
	if got := execute(t, s, "SMEMBERS", "missing"); got != "*0\r\n" {
		t.Fatalf("missing SMEMBERS=%q", got)
	}
}

func TestSetCommandsWrongType(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "plain", "value")

	commands := [][]string{
		{"SADD", "plain", "x"},
		{"SREM", "plain", "x"},
		{"SISMEMBER", "plain", "x"},
		{"SCARD", "plain"},
		{"SMEMBERS", "plain"},
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

func TestSetRenamePreservesTypeAndTTL(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SADD", "source", "a", "b")
	execute(t, s, "PEXPIRE", "source", "60000")

	if got := execute(t, s, "RENAME", "source", "target"); got != "+OK\r\n" {
		t.Fatalf("RENAME=%q", got)
	}
	if got := execute(t, s, "TYPE", "target"); got != "+set\r\n" {
		t.Fatalf("TYPE target=%q", got)
	}
	if got := execute(t, s, "SISMEMBER", "target", "a"); got != ":1\r\n" {
		t.Fatalf("renamed SISMEMBER=%q", got)
	}
	pttl := execute(t, s, "PTTL", "target")
	if pttl == ":-1\r\n" || pttl == ":-2\r\n" {
		t.Fatalf("renamed SET lost TTL: %q", pttl)
	}
}
