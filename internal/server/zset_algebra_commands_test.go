package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestZSetAlgebraReadCommands(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "ZADD", "a", "1", "one", "2", "two")
	execute(t, s, "ZADD", "b", "3", "one", "4", "three")

	if got := execute(t, s, "ZUNION", "2", "a", "b", "WITHSCORES"); got != "*6\r\n$3\r\ntwo\r\n$1\r\n2\r\n$3\r\none\r\n$1\r\n4\r\n$5\r\nthree\r\n$1\r\n4\r\n" {
		t.Fatalf("ZUNION=%q", got)
	}
	if got := execute(t, s, "ZUNION", "2", "a", "b", "WEIGHTS", "2", "3", "WITHSCORES"); got != "*6\r\n$3\r\ntwo\r\n$1\r\n4\r\n$3\r\none\r\n$2\r\n11\r\n$5\r\nthree\r\n$2\r\n12\r\n" {
		t.Fatalf("weighted ZUNION=%q", got)
	}
	if got := execute(t, s, "ZINTER", "2", "a", "b", "WITHSCORES"); got != "*2\r\n$3\r\none\r\n$1\r\n4\r\n" {
		t.Fatalf("ZINTER=%q", got)
	}
	if got := execute(t, s, "ZINTER", "2", "a", "b", "WEIGHTS", "2", "3", "AGGREGATE", "MAX", "WITHSCORES"); got != "*2\r\n$3\r\none\r\n$1\r\n9\r\n" {
		t.Fatalf("weighted ZINTER MAX=%q", got)
	}
	if got := execute(t, s, "ZDIFF", "2", "a", "b", "WITHSCORES"); got != "*2\r\n$3\r\ntwo\r\n$1\r\n2\r\n" {
		t.Fatalf("ZDIFF=%q", got)
	}
	if got := execute(t, s, "ZINTERCARD", "2", "a", "b"); got != ":1\r\n" {
		t.Fatalf("ZINTERCARD=%q", got)
	}
	if got := execute(t, s, "ZINTERCARD", "2", "a", "b", "LIMIT", "1"); got != ":1\r\n" {
		t.Fatalf("ZINTERCARD LIMIT=%q", got)
	}
}

func TestZSetAlgebraCountAggregateAndSetSource(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "ZADD", "a", "1", "one", "2", "two")
	execute(t, s, "ZADD", "b", "3", "one", "4", "three")

	if got := execute(t, s, "ZUNION", "2", "a", "b", "WEIGHTS", "5", "7", "AGGREGATE", "COUNT", "WITHSCORES"); got != "*6\r\n$3\r\ntwo\r\n$1\r\n5\r\n$5\r\nthree\r\n$1\r\n7\r\n$3\r\none\r\n$2\r\n12\r\n" {
		t.Fatalf("ZUNION COUNT=%q", got)
	}

	execute(t, s, "SADD", "plain", "two", "extra")
	if got := execute(t, s, "ZUNION", "2", "a", "plain", "WITHSCORES"); got != "*6\r\n$5\r\nextra\r\n$1\r\n1\r\n$3\r\none\r\n$1\r\n1\r\n$3\r\ntwo\r\n$1\r\n3\r\n" {
		t.Fatalf("mixed SET/ZSET union=%q", got)
	}
}

func TestZSetAlgebraStoreCommands(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "ZADD", "a", "1", "one", "2", "two")
	execute(t, s, "ZADD", "b", "3", "one", "4", "three")
	execute(t, s, "ZADD", "dest", "99", "old")
	execute(t, s, "PEXPIRE", "dest", "60000")

	if got := execute(t, s, "ZUNIONSTORE", "dest", "2", "a", "b", "WEIGHTS", "2", "3"); got != ":3\r\n" {
		t.Fatalf("ZUNIONSTORE=%q", got)
	}
	if got := execute(t, s, "TYPE", "dest"); got != "+zset\r\n" {
		t.Fatalf("TYPE=%q", got)
	}
	if got := execute(t, s, "PTTL", "dest"); got != ":-1\r\n" {
		t.Fatalf("STORE did not clear TTL: %q", got)
	}
	if got := execute(t, s, "ZRANGE", "dest", "0", "-1", "WITHSCORES"); got != "*6\r\n$3\r\ntwo\r\n$1\r\n4\r\n$3\r\none\r\n$2\r\n11\r\n$5\r\nthree\r\n$2\r\n12\r\n" {
		t.Fatalf("stored union=%q", got)
	}

	if got := execute(t, s, "ZINTERSTORE", "inter", "2", "a", "b"); got != ":1\r\n" {
		t.Fatalf("ZINTERSTORE=%q", got)
	}
	if got := execute(t, s, "ZDIFFSTORE", "diff", "2", "a", "b"); got != ":1\r\n" {
		t.Fatalf("ZDIFFSTORE=%q", got)
	}
	if got := execute(t, s, "ZINTERSTORE", "inter", "2", "a", "missing"); got != ":0\r\n" {
		t.Fatalf("empty ZINTERSTORE=%q", got)
	}
	if got := execute(t, s, "TYPE", "inter"); got != "+none\r\n" {
		t.Fatalf("empty result did not delete destination: %q", got)
	}
}

func TestZSetAlgebraStoreDestinationMayBeSource(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "ZADD", "a", "1", "x", "2", "y")
	execute(t, s, "ZADD", "b", "3", "y", "4", "z")

	if got := execute(t, s, "ZUNIONSTORE", "a", "2", "a", "b"); got != ":3\r\n" {
		t.Fatalf("overlapping ZUNIONSTORE=%q", got)
	}
	if got := execute(t, s, "ZRANGE", "a", "0", "-1", "WITHSCORES"); got != "*6\r\n$1\r\nx\r\n$1\r\n1\r\n$1\r\nz\r\n$1\r\n4\r\n$1\r\ny\r\n$1\r\n5\r\n" {
		t.Fatalf("overlapping destination=%q", got)
	}
}

func TestZSetAlgebraWrongTypeAndSyntax(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "ZADD", "a", "1", "one")
	execute(t, s, "ZADD", "dest", "9", "old")
	execute(t, s, "SET", "bad", "value")

	for _, command := range [][]string{
		{"ZUNION", "2", "a", "bad"},
		{"ZINTER", "2", "a", "bad"},
		{"ZDIFF", "2", "a", "bad"},
		{"ZINTERCARD", "2", "a", "bad"},
		{"ZUNIONSTORE", "dest", "2", "a", "bad"},
		{"ZINTERSTORE", "dest", "2", "a", "bad"},
		{"ZDIFFSTORE", "dest", "2", "a", "bad"},
	} {
		args := make([][]byte, len(command))
		for i := range command { args[i] = []byte(command[i]) }
		_, err := s.Execute(args)
		if err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") {
			t.Fatalf("%v err=%v", command, err)
		}
	}
	if got := execute(t, s, "ZRANGE", "dest", "0", "-1", "WITHSCORES"); got != "*2\r\n$3\r\nold\r\n$1\r\n9\r\n" {
		t.Fatalf("destination changed after WRONGTYPE=%q", got)
	}

	bad := [][][]byte{
		{[]byte("ZUNION"), []byte("0")},
		{[]byte("ZUNION"), []byte("2"), []byte("a")},
		{[]byte("ZUNION"), []byte("1"), []byte("a"), []byte("WEIGHTS")},
		{[]byte("ZDIFF"), []byte("1"), []byte("a"), []byte("WEIGHTS"), []byte("2")},
		{[]byte("ZINTERCARD"), []byte("1"), []byte("a"), []byte("LIMIT"), []byte("-1")},
	}
	for _, args := range bad {
		if _, err := s.Execute(args); err == nil {
			t.Fatalf("expected error for %q", args)
		}
	}
}
