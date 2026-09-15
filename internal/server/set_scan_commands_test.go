package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestSetScanAndMultiMemberCommands(t *testing.T) {
	s := New(engine.New())
	if got := execute(t, s, "SADD", "letters", "bb", "aa", "ba", "ab"); got != ":4\r\n" {
		t.Fatalf("SADD=%q", got)
	}

	if got := execute(t, s, "SMISMEMBER", "letters", "aa", "missing", "bb"); got != "*3\r\n:1\r\n:0\r\n:1\r\n" {
		t.Fatalf("SMISMEMBER=%q", got)
	}

	if got := execute(t, s, "SSCAN", "letters", "0", "COUNT", "2"); got != "*2\r\n$1\r\n2\r\n*2\r\n$2\r\naa\r\n$2\r\nab\r\n" {
		t.Fatalf("SSCAN first=%q", got)
	}
	if got := execute(t, s, "SSCAN", "letters", "2", "MATCH", "b*", "COUNT", "2"); got != "*2\r\n$1\r\n0\r\n*2\r\n$2\r\nba\r\n$2\r\nbb\r\n" {
		t.Fatalf("SSCAN second=%q", got)
	}

	if got := execute(t, s, "SMISMEMBER", "missing", "a", "b"); got != "*2\r\n:0\r\n:0\r\n" {
		t.Fatalf("missing SMISMEMBER=%q", got)
	}
	if got := execute(t, s, "SSCAN", "missing", "0"); got != "*2\r\n$1\r\n0\r\n*0\r\n" {
		t.Fatalf("missing SSCAN=%q", got)
	}
}

func TestSetScanCommandErrors(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "plain", "value")

	for _, command := range [][]string{
		{"SMISMEMBER", "plain", "a"},
		{"SSCAN", "plain", "0"},
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

	for _, command := range [][]string{
		{"SSCAN", "missing", "not-a-cursor"},
		{"SSCAN", "missing", "0", "COUNT", "0"},
		{"SSCAN", "missing", "0", "MATCH"},
		{"SSCAN", "missing", "0", "UNKNOWN", "x"},
	} {
		args := make([][]byte, len(command))
		for i := range command {
			args[i] = []byte(command[i])
		}
		if _, err := s.Execute(args); err == nil {
			t.Fatalf("%v expected error", command)
		}
	}
}
