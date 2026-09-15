package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestZSetLexCommands(t *testing.T) {
	s := New(engine.New())
	if got := execute(t, s, "ZADD", "z", "0", "alpha", "0", "beta", "0", "delta", "0", "gamma"); got != ":4\r\n" {
		t.Fatalf("ZADD=%q", got)
	}
	if got := execute(t, s, "ZLEXCOUNT", "z", "[beta", "(gamma"); got != ":2\r\n" {
		t.Fatalf("ZLEXCOUNT=%q", got)
	}
	if got := execute(t, s, "ZRANGEBYLEX", "z", "[beta", "[gamma"); got != "*3\r\n$4\r\nbeta\r\n$5\r\ndelta\r\n$5\r\ngamma\r\n" {
		t.Fatalf("ZRANGEBYLEX=%q", got)
	}
	if got := execute(t, s, "ZRANGEBYLEX", "z", "-", "+", "LIMIT", "1", "2"); got != "*2\r\n$4\r\nbeta\r\n$5\r\ndelta\r\n" {
		t.Fatalf("ZRANGEBYLEX LIMIT=%q", got)
	}
	if got := execute(t, s, "ZREVRANGEBYLEX", "z", "[gamma", "[beta"); got != "*3\r\n$5\r\ngamma\r\n$5\r\ndelta\r\n$4\r\nbeta\r\n" {
		t.Fatalf("ZREVRANGEBYLEX=%q", got)
	}
}

func TestZRangeByLexModernSyntax(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "ZADD", "z", "7", "a", "7", "b", "7", "c", "7", "d")
	if got := execute(t, s, "ZRANGE", "z", "[b", "[d", "BYLEX"); got != "*3\r\n$1\r\nb\r\n$1\r\nc\r\n$1\r\nd\r\n" {
		t.Fatalf("ZRANGE BYLEX=%q", got)
	}
	if got := execute(t, s, "ZRANGE", "z", "[d", "[a", "BYLEX", "REV", "LIMIT", "1", "2", "WITHSCORES"); got != "*4\r\n$1\r\nc\r\n$1\r\n7\r\n$1\r\nb\r\n$1\r\n7\r\n" {
		t.Fatalf("ZRANGE BYLEX REV=%q", got)
	}
}

func TestZRemRangeByLexTTLAndErrors(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "ZADD", "z", "0", "a", "0", "b", "0", "c", "0", "d")
	execute(t, s, "PEXPIRE", "z", "60000")
	if got := execute(t, s, "ZREMRANGEBYLEX", "z", "[b", "[c"); got != ":2\r\n" {
		t.Fatalf("remove=%q", got)
	}
	if got := execute(t, s, "ZRANGE", "z", "0", "-1"); got != "*2\r\n$1\r\na\r\n$1\r\nd\r\n" {
		t.Fatalf("remaining=%q", got)
	}
	pttl := execute(t, s, "PTTL", "z")
	if pttl == ":-1\r\n" || pttl == ":-2\r\n" {
		t.Fatalf("ttl lost: %q", pttl)
	}
	if _, err := s.Execute([][]byte{[]byte("ZRANGEBYLEX"), []byte("z"), []byte("bad"), []byte("+")}); err == nil || !strings.Contains(err.Error(), "not valid string range item") {
		t.Fatalf("invalid bound err=%v", err)
	}
	execute(t, s, "SET", "plain", "value")
	if _, err := s.Execute([][]byte{[]byte("ZLEXCOUNT"), []byte("plain"), []byte("-"), []byte("+")}); err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") {
		t.Fatalf("wrong type err=%v", err)
	}
}
