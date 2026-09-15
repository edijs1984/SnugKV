package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestZSetCommands(t *testing.T) {
	s := New(engine.New())
	if got := execute(t, s, "ZADD", "myz", "2", "b", "1", "c", "1", "a"); got != ":3\r\n" {
		t.Fatalf("ZADD=%q", got)
	}
	if got := execute(t, s, "TYPE", "myz"); got != "+zset\r\n" {
		t.Fatalf("TYPE=%q", got)
	}
	if got := execute(t, s, "ZCARD", "myz"); got != ":3\r\n" {
		t.Fatalf("ZCARD=%q", got)
	}
	if got := execute(t, s, "ZRANGE", "myz", "0", "-1"); got != "*3\r\n$1\r\na\r\n$1\r\nc\r\n$1\r\nb\r\n" {
		t.Fatalf("ZRANGE=%q", got)
	}
	if got := execute(t, s, "ZRANGE", "myz", "0", "-1", "WITHSCORES"); got != "*6\r\n$1\r\na\r\n$1\r\n1\r\n$1\r\nc\r\n$1\r\n1\r\n$1\r\nb\r\n$1\r\n2\r\n" {
		t.Fatalf("ZRANGE WITHSCORES=%q", got)
	}
	if got := execute(t, s, "ZREVRANGE", "myz", "0", "1"); got != "*2\r\n$1\r\nb\r\n$1\r\nc\r\n" {
		t.Fatalf("ZREVRANGE=%q", got)
	}
	if got := execute(t, s, "ZSCORE", "myz", "c"); got != "$1\r\n1\r\n" {
		t.Fatalf("ZSCORE=%q", got)
	}
	if got := execute(t, s, "ZRANK", "myz", "c"); got != ":1\r\n" {
		t.Fatalf("ZRANK=%q", got)
	}
	if got := execute(t, s, "ZREVRANK", "myz", "c"); got != ":1\r\n" {
		t.Fatalf("ZREVRANK=%q", got)
	}
	if got := execute(t, s, "ZRANK", "myz", "c", "WITHSCORE"); got != "*2\r\n:1\r\n$1\r\n1\r\n" {
		t.Fatalf("ZRANK WITHSCORE=%q", got)
	}
	if got := execute(t, s, "ZREM", "myz", "c", "missing"); got != ":1\r\n" {
		t.Fatalf("ZREM=%q", got)
	}
	if got := execute(t, s, "ZRANGE", "myz", "0", "-1"); got != "*2\r\n$1\r\na\r\n$1\r\nb\r\n" {
		t.Fatalf("remaining=%q", got)
	}
}

func TestZAddOptionsAndIncrement(t *testing.T) {
	s := New(engine.New())
	if got := execute(t, s, "ZADD", "z", "1", "a"); got != ":1\r\n" {
		t.Fatalf("initial=%q", got)
	}
	if got := execute(t, s, "ZADD", "z", "NX", "2", "a"); got != ":0\r\n" {
		t.Fatalf("NX=%q", got)
	}
	if got := execute(t, s, "ZADD", "z", "XX", "CH", "2", "a"); got != ":1\r\n" {
		t.Fatalf("XX CH=%q", got)
	}
	if got := execute(t, s, "ZADD", "z", "GT", "CH", "1", "a"); got != ":0\r\n" {
		t.Fatalf("GT lower=%q", got)
	}
	if got := execute(t, s, "ZADD", "z", "LT", "CH", "1", "a"); got != ":1\r\n" {
		t.Fatalf("LT lower=%q", got)
	}
	if got := execute(t, s, "ZADD", "z", "INCR", "1.5", "a"); got != "$3\r\n2.5\r\n" {
		t.Fatalf("INCR=%q", got)
	}
	if got := execute(t, s, "ZADD", "z", "NX", "INCR", "1", "a"); got != "$-1\r\n" {
		t.Fatalf("NX INCR=%q", got)
	}
	if got := execute(t, s, "ZSCORE", "z", "a"); got != "$3\r\n2.5\r\n" {
		t.Fatalf("score=%q", got)
	}
}

func TestZSetMissingWrongTypeAndSyntax(t *testing.T) {
	s := New(engine.New())
	if got := execute(t, s, "ZCARD", "missing"); got != ":0\r\n" {
		t.Fatalf("missing card=%q", got)
	}
	if got := execute(t, s, "ZSCORE", "missing", "x"); got != "$-1\r\n" {
		t.Fatalf("missing score=%q", got)
	}
	if got := execute(t, s, "ZRANK", "missing", "x"); got != "$-1\r\n" {
		t.Fatalf("missing rank=%q", got)
	}
	if got := execute(t, s, "ZRANGE", "missing", "0", "-1"); got != "*0\r\n" {
		t.Fatalf("missing range=%q", got)
	}
	execute(t, s, "SET", "plain", "value")
	commands := [][]string{
		{"ZADD", "plain", "1", "a"},
		{"ZREM", "plain", "a"},
		{"ZSCORE", "plain", "a"},
		{"ZCARD", "plain"},
		{"ZRANK", "plain", "a"},
		{"ZREVRANK", "plain", "a"},
		{"ZRANGE", "plain", "0", "-1"},
		{"ZREVRANGE", "plain", "0", "-1"},
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
	bad := [][][]byte{
		{[]byte("ZADD"), []byte("z"), []byte("NX"), []byte("XX"), []byte("1"), []byte("a")},
		{[]byte("ZADD"), []byte("z"), []byte("INCR"), []byte("1"), []byte("a"), []byte("2"), []byte("b")},
		{[]byte("ZADD"), []byte("z"), []byte("nan"), []byte("a")},
		{[]byte("ZRANGE"), []byte("z"), []byte("x"), []byte("1")},
	}
	for _, args := range bad {
		if _, err := s.Execute(args); err == nil {
			t.Fatalf("expected error for %q", args)
		}
	}
}

func TestZSetRenamePreservesTypeTTLAndOrder(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "ZADD", "source", "2", "b", "1", "a")
	execute(t, s, "PEXPIRE", "source", "60000")
	if got := execute(t, s, "RENAME", "source", "target"); got != "+OK\r\n" {
		t.Fatalf("RENAME=%q", got)
	}
	if got := execute(t, s, "TYPE", "target"); got != "+zset\r\n" {
		t.Fatalf("TYPE=%q", got)
	}
	if got := execute(t, s, "ZRANGE", "target", "0", "-1", "WITHSCORES"); got != "*4\r\n$1\r\na\r\n$1\r\n1\r\n$1\r\nb\r\n$1\r\n2\r\n" {
		t.Fatalf("renamed range=%q", got)
	}
	pttl := execute(t, s, "PTTL", "target")
	if pttl == ":-1\r\n" || pttl == ":-2\r\n" {
		t.Fatalf("renamed ZSET lost TTL: %q", pttl)
	}
}
