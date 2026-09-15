package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestListMoveCommands(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "RPUSH", "source", "a", "b", "c")
	execute(t, s, "RPUSH", "dest", "x", "y")
	execute(t, s, "PEXPIRE", "source", "60000")
	execute(t, s, "PEXPIRE", "dest", "120000")

	if got := execute(t, s, "LMOVE", "source", "dest", "RIGHT", "LEFT"); got != "$1\r\nc\r\n" {
		t.Fatalf("LMOVE RIGHT LEFT=%q", got)
	}
	if got := execute(t, s, "LRANGE", "source", "0", "-1"); got != "*2\r\n$1\r\na\r\n$1\r\nb\r\n" {
		t.Fatalf("source after LMOVE=%q", got)
	}
	if got := execute(t, s, "LRANGE", "dest", "0", "-1"); got != "*3\r\n$1\r\nc\r\n$1\r\nx\r\n$1\r\ny\r\n" {
		t.Fatalf("dest after LMOVE=%q", got)
	}

	if got := execute(t, s, "LMOVE", "source", "dest", "LEFT", "RIGHT"); got != "$1\r\na\r\n" {
		t.Fatalf("LMOVE LEFT RIGHT=%q", got)
	}
	if got := execute(t, s, "RPOPLPUSH", "source", "dest"); got != "$1\r\nb\r\n" {
		t.Fatalf("RPOPLPUSH=%q", got)
	}
	if got := execute(t, s, "TYPE", "source"); got != "+none\r\n" {
		t.Fatalf("source type after final move=%q", got)
	}
	if got := execute(t, s, "LRANGE", "dest", "0", "-1"); got != "*5\r\n$1\r\nb\r\n$1\r\nc\r\n$1\r\nx\r\n$1\r\ny\r\n$1\r\na\r\n" {
		t.Fatalf("dest final=%q", got)
	}
	if pttl := execute(t, s, "PTTL", "dest"); pttl == ":-1\r\n" || pttl == ":-2\r\n" {
		t.Fatalf("destination TTL lost: %q", pttl)
	}
	if got := execute(t, s, "LMOVE", "missing", "dest", "LEFT", "RIGHT"); got != "$-1\r\n" {
		t.Fatalf("missing source LMOVE=%q", got)
	}
}

func TestListMoveSameKeyRotation(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "RPUSH", "list", "a", "b", "c")
	execute(t, s, "PEXPIRE", "list", "60000")

	if got := execute(t, s, "LMOVE", "list", "list", "RIGHT", "LEFT"); got != "$1\r\nc\r\n" {
		t.Fatalf("same-key LMOVE=%q", got)
	}
	if got := execute(t, s, "LRANGE", "list", "0", "-1"); got != "*3\r\n$1\r\nc\r\n$1\r\na\r\n$1\r\nb\r\n" {
		t.Fatalf("same-key rotation=%q", got)
	}
	if got := execute(t, s, "RPOPLPUSH", "list", "list"); got != "$1\r\nb\r\n" {
		t.Fatalf("same-key RPOPLPUSH=%q", got)
	}
	if got := execute(t, s, "LRANGE", "list", "0", "-1"); got != "*3\r\n$1\r\nb\r\n$1\r\nc\r\n$1\r\na\r\n" {
		t.Fatalf("same-key RPOPLPUSH rotation=%q", got)
	}
	if got := execute(t, s, "LMOVE", "list", "list", "LEFT", "LEFT"); got != "$1\r\nb\r\n" {
		t.Fatalf("same-side LMOVE=%q", got)
	}
	if got := execute(t, s, "LRANGE", "list", "0", "-1"); got != "*3\r\n$1\r\nb\r\n$1\r\nc\r\n$1\r\na\r\n" {
		t.Fatalf("same-side LMOVE changed list=%q", got)
	}
	if pttl := execute(t, s, "PTTL", "list"); pttl == ":-1\r\n" || pttl == ":-2\r\n" {
		t.Fatalf("same-key move lost TTL: %q", pttl)
	}
}

func TestListMoveSyntaxAndWrongType(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "plain", "value")

	// Missing source returns nil before destination type matters.
	if got := execute(t, s, "LMOVE", "missing", "plain", "RIGHT", "LEFT"); got != "$-1\r\n" {
		t.Fatalf("missing source with wrong-type destination=%q", got)
	}

	execute(t, s, "RPUSH", "source", "a", "b")
	for _, command := range [][]string{
		{"LMOVE", "source", "plain", "RIGHT", "LEFT"},
		{"RPOPLPUSH", "source", "plain"},
		{"LMOVE", "plain", "source", "RIGHT", "LEFT"},
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
	if got := execute(t, s, "LRANGE", "source", "0", "-1"); got != "*2\r\n$1\r\na\r\n$1\r\nb\r\n" {
		t.Fatalf("wrong-type move mutated source=%q", got)
	}

	for _, command := range [][]byte{
		[]byte("LMOVE source dest MIDDLE LEFT"),
		[]byte("LMOVE source dest LEFT MIDDLE"),
	} {
		parts := strings.Fields(string(command))
		args := make([][]byte, len(parts))
		for i := range parts {
			args[i] = []byte(parts[i])
		}
		_, err := s.Execute(args)
		if err == nil || err.Error() != "ERR syntax error" {
			t.Fatalf("%q err=%v", command, err)
		}
	}
}
