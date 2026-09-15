package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestZSetPopAndMultiScoreCommands(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "ZADD", "z", "1", "a", "2", "b", "3", "c")
	if got := execute(t, s, "ZMSCORE", "z", "b", "missing", "a"); got != "*3\r\n$1\r\n2\r\n$-1\r\n$1\r\n1\r\n" {
		t.Fatalf("ZMSCORE=%q", got)
	}
	if got := execute(t, s, "ZPOPMIN", "z"); got != "*2\r\n$1\r\na\r\n$1\r\n1\r\n" {
		t.Fatalf("ZPOPMIN=%q", got)
	}
	if got := execute(t, s, "ZPOPMAX", "z", "2"); got != "*4\r\n$1\r\nc\r\n$1\r\n3\r\n$1\r\nb\r\n$1\r\n2\r\n" {
		t.Fatalf("ZPOPMAX=%q", got)
	}
	if got := execute(t, s, "TYPE", "z"); got != "+none\r\n" { t.Fatalf("TYPE=%q", got) }
}

func TestZMPopChoosesFirstNonEmptyAndNestedReply(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "ZADD", "second", "1", "a", "2", "b", "3", "c")
	if got := execute(t, s, "ZMPOP", "2", "missing", "second", "MAX", "COUNT", "2"); got != "*2\r\n$6\r\nsecond\r\n*2\r\n*2\r\n$1\r\nc\r\n$1\r\n3\r\n*2\r\n$1\r\nb\r\n$1\r\n2\r\n" {
		t.Fatalf("ZMPOP=%q", got)
	}
	// COUNT is optional and defaults to one.
	if got := execute(t, s, "ZMPOP", "1", "second", "MIN"); got != "*2\r\n$6\r\nsecond\r\n*1\r\n*2\r\n$1\r\na\r\n$1\r\n1\r\n" {
		t.Fatalf("ZMPOP default count=%q", got)
	}
	if got := execute(t, s, "ZMPOP", "1", "second", "MIN"); got != "*-1\r\n" { t.Fatalf("empty ZMPOP=%q", got) }
}

func TestZRandMemberAndZScanCommands(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "ZADD", "z", "1", "alpha", "2", "beta", "3", "gamma")
	got := execute(t, s, "ZRANDMEMBER", "z", "3", "WITHSCORES")
	if !strings.HasPrefix(got, "*6\r\n") { t.Fatalf("ZRANDMEMBER=%q", got) }
	got = execute(t, s, "ZRANDMEMBER", "z", "-5")
	if !strings.HasPrefix(got, "*5\r\n") { t.Fatalf("negative ZRANDMEMBER=%q", got) }
	got = execute(t, s, "ZSCAN", "z", "0", "MATCH", "*a*", "COUNT", "1")
	if !strings.HasPrefix(got, "*2\r\n$") || !strings.Contains(got, "*2\r\n$") { t.Fatalf("ZSCAN=%q", got) }
}

func TestZRangeStoreRankScoreLexAndTTL(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "ZADD", "src", "1", "alpha", "2", "beta", "3", "delta", "4", "gamma")
	execute(t, s, "ZADD", "dest", "99", "old")
	execute(t, s, "PEXPIRE", "dest", "60000")
	if got := execute(t, s, "ZRANGESTORE", "dest", "src", "1", "2"); got != ":2\r\n" { t.Fatalf("rank=%q", got) }
	if got := execute(t, s, "PTTL", "dest"); got != ":-1\r\n" { t.Fatalf("ttl=%q", got) }
	if got := execute(t, s, "ZRANGE", "dest", "0", "-1", "WITHSCORES"); got != "*4\r\n$4\r\nbeta\r\n$1\r\n2\r\n$5\r\ndelta\r\n$1\r\n3\r\n" { t.Fatalf("rank dest=%q", got) }
	if got := execute(t, s, "ZRANGESTORE", "score", "src", "4", "(1", "BYSCORE", "REV", "LIMIT", "1", "2"); got != ":2\r\n" { t.Fatalf("score=%q", got) }
	if got := execute(t, s, "ZRANGE", "score", "0", "-1"); got != "*2\r\n$4\r\nbeta\r\n$5\r\ndelta\r\n" { t.Fatalf("score dest=%q", got) }
	if got := execute(t, s, "ZRANGESTORE", "lex", "src", "[gamma", "[alpha", "BYLEX", "REV", "LIMIT", "1", "2"); got != ":2\r\n" { t.Fatalf("lex=%q", got) }
}

func TestZSetOpsWrongType(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "plain", "x")
	for _, command := range [][]string{{"ZPOPMIN", "plain"}, {"ZMSCORE", "plain", "x"}, {"ZRANDMEMBER", "plain"}, {"ZSCAN", "plain", "0"}, {"ZRANGESTORE", "dest", "plain", "0", "-1"}} {
		args := make([][]byte, len(command)); for i := range command { args[i] = []byte(command[i]) }
		_, err := s.Execute(args)
		if err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") { t.Fatalf("%v err=%v", command, err) }
	}
}
