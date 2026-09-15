package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestZSetScoreRangeCommands(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "ZADD", "z", "1", "a", "2", "b", "2", "c", "3", "d", "4", "e")

	if got := execute(t, s, "ZCOUNT", "z", "2", "3"); got != ":3\r\n" {
		t.Fatalf("ZCOUNT=%q", got)
	}
	if got := execute(t, s, "ZCOUNT", "z", "(2", "+inf"); got != ":2\r\n" {
		t.Fatalf("ZCOUNT exclusive=%q", got)
	}
	if got := execute(t, s, "ZRANGEBYSCORE", "z", "(1", "3", "WITHSCORES"); got != "*6\r\n$1\r\nb\r\n$1\r\n2\r\n$1\r\nc\r\n$1\r\n2\r\n$1\r\nd\r\n$1\r\n3\r\n" {
		t.Fatalf("ZRANGEBYSCORE=%q", got)
	}
	if got := execute(t, s, "ZRANGEBYSCORE", "z", "-inf", "+inf", "LIMIT", "1", "2"); got != "*2\r\n$1\r\nb\r\n$1\r\nc\r\n" {
		t.Fatalf("ZRANGEBYSCORE LIMIT=%q", got)
	}
	if got := execute(t, s, "ZREVRANGEBYSCORE", "z", "3", "2"); got != "*3\r\n$1\r\nd\r\n$1\r\nc\r\n$1\r\nb\r\n" {
		t.Fatalf("ZREVRANGEBYSCORE=%q", got)
	}
}

func TestZRangeModernScoreModeAndRev(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "ZADD", "z", "1", "a", "2", "b", "2", "c", "3", "d", "4", "e")

	if got := execute(t, s, "ZRANGE", "z", "2", "4", "BYSCORE"); got != "*4\r\n$1\r\nb\r\n$1\r\nc\r\n$1\r\nd\r\n$1\r\ne\r\n" {
		t.Fatalf("ZRANGE BYSCORE=%q", got)
	}
	if got := execute(t, s, "ZRANGE", "z", "4", "(1", "BYSCORE", "REV", "LIMIT", "1", "2", "WITHSCORES"); got != "*4\r\n$1\r\nd\r\n$1\r\n3\r\n$1\r\nc\r\n$1\r\n2\r\n" {
		t.Fatalf("ZRANGE BYSCORE REV=%q", got)
	}
	if got := execute(t, s, "ZRANGE", "z", "0", "1", "REV"); got != "*2\r\n$1\r\ne\r\n$1\r\nd\r\n" {
		t.Fatalf("ZRANGE REV=%q", got)
	}
}

func TestZIncrByAndRangeRemovals(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "ZADD", "z", "1", "a", "2", "b", "3", "c", "4", "d", "5", "e")
	execute(t, s, "PEXPIRE", "z", "60000")

	if got := execute(t, s, "ZINCRBY", "z", "1.5", "a"); got != "$3\r\n2.5\r\n" {
		t.Fatalf("ZINCRBY=%q", got)
	}
	if got := execute(t, s, "ZREMRANGEBYSCORE", "z", "(3", "+inf"); got != ":2\r\n" {
		t.Fatalf("ZREMRANGEBYSCORE=%q", got)
	}
	if got := execute(t, s, "ZREMRANGEBYRANK", "z", "-1", "-1"); got != ":1\r\n" {
		t.Fatalf("ZREMRANGEBYRANK=%q", got)
	}
	if got := execute(t, s, "ZRANGE", "z", "0", "-1", "WITHSCORES"); got != "*4\r\n$1\r\nb\r\n$1\r\n2\r\n$1\r\na\r\n$3\r\n2.5\r\n" {
		t.Fatalf("remaining=%q", got)
	}
	pttl := execute(t, s, "PTTL", "z")
	if pttl == ":-1\r\n" || pttl == ":-2\r\n" {
		t.Fatalf("range mutation lost TTL: %q", pttl)
	}
}

func TestZSetScoreRangeMissingWrongTypeAndSyntax(t *testing.T) {
	s := New(engine.New())
	if got := execute(t, s, "ZCOUNT", "missing", "-inf", "+inf"); got != ":0\r\n" {
		t.Fatalf("missing count=%q", got)
	}
	if got := execute(t, s, "ZRANGEBYSCORE", "missing", "-inf", "+inf"); got != "*0\r\n" {
		t.Fatalf("missing range=%q", got)
	}
	execute(t, s, "SET", "plain", "value")
	commands := [][]string{
		{"ZINCRBY", "plain", "1", "a"},
		{"ZCOUNT", "plain", "-inf", "+inf"},
		{"ZRANGEBYSCORE", "plain", "-inf", "+inf"},
		{"ZREVRANGEBYSCORE", "plain", "+inf", "-inf"},
		{"ZREMRANGEBYRANK", "plain", "0", "1"},
		{"ZREMRANGEBYSCORE", "plain", "-inf", "+inf"},
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
		{[]byte("ZCOUNT"), []byte("z"), []byte("("), []byte("2")},
		{[]byte("ZRANGEBYSCORE"), []byte("z"), []byte("1"), []byte("2"), []byte("LIMIT"), []byte("x"), []byte("1")},
		{[]byte("ZRANGE"), []byte("z"), []byte("0"), []byte("1"), []byte("LIMIT"), []byte("0"), []byte("1")},
		{[]byte("ZRANGE"), []byte("z"), []byte("0"), []byte("1"), []byte("BYLEX")},
	}
	for _, args := range bad {
		if _, err := s.Execute(args); err == nil {
			t.Fatalf("expected error for %q", args)
		}
	}
}
