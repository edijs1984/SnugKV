package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestStreamCommands(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "XADD", "events", "1-0", "type", "created", "id", "42"); got != "$3\r\n1-0\r\n" {
		t.Fatalf("XADD first=%q", got)
	}
	if got := execute(t, s, "XADD", "events", "2-0", "type", "updated"); got != "$3\r\n2-0\r\n" {
		t.Fatalf("XADD second=%q", got)
	}
	if got := execute(t, s, "TYPE", "events"); got != "+stream\r\n" {
		t.Fatalf("TYPE=%q", got)
	}
	if got := execute(t, s, "XLEN", "events"); got != ":2\r\n" {
		t.Fatalf("XLEN=%q", got)
	}

	wantRange := "*2\r\n" +
		"*2\r\n$3\r\n1-0\r\n*4\r\n$4\r\ntype\r\n$7\r\ncreated\r\n$2\r\nid\r\n$2\r\n42\r\n" +
		"*2\r\n$3\r\n2-0\r\n*2\r\n$4\r\ntype\r\n$7\r\nupdated\r\n"
	if got := execute(t, s, "XRANGE", "events", "-", "+"); got != wantRange {
		t.Fatalf("XRANGE=%q want=%q", got, wantRange)
	}

	wantReverse := "*1\r\n*2\r\n$3\r\n2-0\r\n*2\r\n$4\r\ntype\r\n$7\r\nupdated\r\n"
	if got := execute(t, s, "XREVRANGE", "events", "+", "-", "COUNT", "1"); got != wantReverse {
		t.Fatalf("XREVRANGE=%q", got)
	}
	if got := execute(t, s, "XDEL", "events", "1-0"); got != ":1\r\n" {
		t.Fatalf("XDEL=%q", got)
	}
	if got := execute(t, s, "XTRIM", "events", "MAXLEN", "0"); got != ":1\r\n" {
		t.Fatalf("XTRIM=%q", got)
	}
	if got := execute(t, s, "XLEN", "events"); got != ":0\r\n" {
		t.Fatalf("empty XLEN=%q", got)
	}
	if got := execute(t, s, "TYPE", "events"); got != "+stream\r\n" {
		t.Fatalf("empty TYPE=%q", got)
	}
	if _, err := s.Execute([][]byte{[]byte("XADD"), []byte("events"), []byte("1-0"), []byte("a"), []byte("b")}); err == nil {
		t.Fatal("XADD accepted ID below retained last-generated ID")
	}
}

func TestStreamNoMkStreamAndMaxLen(t *testing.T) {
	s := New(engine.New())
	if got := execute(t, s, "XADD", "missing", "NOMKSTREAM", "*", "a", "b"); got != "$-1\r\n" {
		t.Fatalf("NOMKSTREAM=%q", got)
	}
	for _, id := range []string{"1-0", "2-0", "3-0"} {
		execute(t, s, "XADD", "events", "MAXLEN", "2", id, "v", id)
	}
	if got := execute(t, s, "XLEN", "events"); got != ":2\r\n" {
		t.Fatalf("MAXLEN XLEN=%q", got)
	}
	if got := execute(t, s, "XRANGE", "events", "-", "+", "COUNT", "1"); !strings.Contains(got, "2-0") || strings.Contains(got, "1-0") {
		t.Fatalf("MAXLEN XRANGE=%q", got)
	}
}

func TestStreamWrongTypeAndRename(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "plain", "value")
	for _, command := range [][]string{{"XLEN", "plain"}, {"XADD", "plain", "1-0", "a", "b"}, {"XRANGE", "plain", "-", "+"}} {
		args := make([][]byte, len(command))
		for i := range command {
			args[i] = []byte(command[i])
		}
		_, err := s.Execute(args)
		if err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") {
			t.Fatalf("%v err=%v", command, err)
		}
	}

	execute(t, s, "XADD", "source", "1-0", "a", "b")
	execute(t, s, "PEXPIRE", "source", "60000")
	if got := execute(t, s, "RENAME", "source", "target"); got != "+OK\r\n" {
		t.Fatalf("RENAME=%q", got)
	}
	if got := execute(t, s, "TYPE", "target"); got != "+stream\r\n" {
		t.Fatalf("TYPE target=%q", got)
	}
	if got := execute(t, s, "XLEN", "target"); got != ":1\r\n" {
		t.Fatalf("XLEN target=%q", got)
	}
	pttl := execute(t, s, "PTTL", "target")
	if pttl == ":-1\r\n" || pttl == ":-2\r\n" {
		t.Fatalf("renamed stream lost TTL: %q", pttl)
	}
}
