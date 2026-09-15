package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestListPushXAndSetTrim(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "LPUSHX", "missing", "a"); got != ":0\r\n" {
		t.Fatalf("LPUSHX missing=%q", got)
	}
	if got := execute(t, s, "TYPE", "missing"); got != "+none\r\n" {
		t.Fatalf("LPUSHX created missing key: %q", got)
	}

	execute(t, s, "RPUSH", "list", "a", "b", "c")
	execute(t, s, "PEXPIRE", "list", "60000")
	if got := execute(t, s, "LPUSHX", "list", "x", "y"); got != ":5\r\n" {
		t.Fatalf("LPUSHX=%q", got)
	}
	if got := execute(t, s, "RPUSHX", "list", "d", "e"); got != ":7\r\n" {
		t.Fatalf("RPUSHX=%q", got)
	}
	if got := execute(t, s, "LSET", "list", "-1", "tail"); got != "+OK\r\n" {
		t.Fatalf("LSET=%q", got)
	}
	if got := execute(t, s, "LTRIM", "list", "1", "-2"); got != "+OK\r\n" {
		t.Fatalf("LTRIM=%q", got)
	}
	if got := execute(t, s, "LRANGE", "list", "0", "-1"); got != "*5\r\n$1\r\nx\r\n$1\r\na\r\n$1\r\nb\r\n$1\r\nc\r\n$1\r\nd\r\n" {
		t.Fatalf("trimmed list=%q", got)
	}
	pttl := execute(t, s, "PTTL", "list")
	if pttl == ":-1\r\n" || pttl == ":-2\r\n" {
		t.Fatalf("mutations lost TTL: %q", pttl)
	}

	if _, err := s.Execute([][]byte{[]byte("LSET"), []byte("missing2"), []byte("0"), []byte("x")}); err == nil || !strings.Contains(err.Error(), "no such key") {
		t.Fatalf("missing LSET err=%v", err)
	}
	if _, err := s.Execute([][]byte{[]byte("LSET"), []byte("list"), []byte("99"), []byte("x")}); err == nil || !strings.Contains(err.Error(), "index out of range") {
		t.Fatalf("out-of-range LSET err=%v", err)
	}

	if got := execute(t, s, "LTRIM", "list", "99", "100"); got != "+OK\r\n" {
		t.Fatalf("empty LTRIM=%q", got)
	}
	if got := execute(t, s, "TYPE", "list"); got != "+none\r\n" {
		t.Fatalf("empty LTRIM should delete key: %q", got)
	}
}

func TestListRemoveDirections(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "RPUSH", "positive", "a", "x", "a", "a", "b")
	if got := execute(t, s, "LREM", "positive", "2", "a"); got != ":2\r\n" {
		t.Fatalf("positive LREM=%q", got)
	}
	if got := execute(t, s, "LRANGE", "positive", "0", "-1"); got != "*3\r\n$1\r\nx\r\n$1\r\na\r\n$1\r\nb\r\n" {
		t.Fatalf("positive result=%q", got)
	}

	execute(t, s, "RPUSH", "negative", "a", "x", "a", "a", "b")
	if got := execute(t, s, "LREM", "negative", "-2", "a"); got != ":2\r\n" {
		t.Fatalf("negative LREM=%q", got)
	}
	if got := execute(t, s, "LRANGE", "negative", "0", "-1"); got != "*3\r\n$1\r\na\r\n$1\r\nx\r\n$1\r\nb\r\n" {
		t.Fatalf("negative result=%q", got)
	}

	execute(t, s, "RPUSH", "all", "a", "x", "a")
	if got := execute(t, s, "LREM", "all", "0", "a"); got != ":2\r\n" {
		t.Fatalf("all LREM=%q", got)
	}
	if got := execute(t, s, "LRANGE", "all", "0", "-1"); got != "*1\r\n$1\r\nx\r\n" {
		t.Fatalf("all result=%q", got)
	}
}

func TestListInsertAndPos(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "LINSERT", "missing", "BEFORE", "a", "x"); got != ":0\r\n" {
		t.Fatalf("missing LINSERT=%q", got)
	}
	execute(t, s, "RPUSH", "list", "a", "b", "a", "c", "a", "b", "a")
	if got := execute(t, s, "LINSERT", "list", "BEFORE", "b", "x"); got != ":8\r\n" {
		t.Fatalf("LINSERT before=%q", got)
	}
	if got := execute(t, s, "LINSERT", "list", "AFTER", "missing", "x"); got != ":-1\r\n" {
		t.Fatalf("LINSERT missing pivot=%q", got)
	}

	// Rebuild the canonical LPOS fixture without the inserted element.
	execute(t, s, "DEL", "list")
	execute(t, s, "RPUSH", "list", "a", "b", "a", "c", "a", "b", "a")
	if got := execute(t, s, "LPOS", "list", "a"); got != ":0\r\n" {
		t.Fatalf("LPOS=%q", got)
	}
	if got := execute(t, s, "LPOS", "list", "a", "RANK", "2"); got != ":2\r\n" {
		t.Fatalf("LPOS rank=%q", got)
	}
	if got := execute(t, s, "LPOS", "list", "a", "RANK", "-1"); got != ":6\r\n" {
		t.Fatalf("LPOS negative rank=%q", got)
	}
	if got := execute(t, s, "LPOS", "list", "a", "COUNT", "0"); got != "*4\r\n:0\r\n:2\r\n:4\r\n:6\r\n" {
		t.Fatalf("LPOS count all=%q", got)
	}
	if got := execute(t, s, "LPOS", "list", "a", "RANK", "2", "COUNT", "2"); got != "*2\r\n:2\r\n:4\r\n" {
		t.Fatalf("LPOS rank/count=%q", got)
	}
	if got := execute(t, s, "LPOS", "list", "a", "RANK", "-2", "COUNT", "2"); got != "*2\r\n:4\r\n:2\r\n" {
		t.Fatalf("LPOS negative rank/count=%q", got)
	}
	if got := execute(t, s, "LPOS", "list", "c", "MAXLEN", "2"); got != "$-1\r\n" {
		t.Fatalf("LPOS maxlen=%q", got)
	}
	if got := execute(t, s, "LPOS", "missing", "a", "COUNT", "0"); got != "*0\r\n" {
		t.Fatalf("missing LPOS count=%q", got)
	}
}

func TestListCompatibilityWrongType(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "plain", "value")
	commands := [][]string{
		{"LPUSHX", "plain", "x"},
		{"RPUSHX", "plain", "x"},
		{"LSET", "plain", "0", "x"},
		{"LTRIM", "plain", "0", "1"},
		{"LREM", "plain", "0", "x"},
		{"LINSERT", "plain", "BEFORE", "x", "y"},
		{"LPOS", "plain", "x"},
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
