package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestHashCommandSubset(t *testing.T) {
	s := New(engine.New())

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"HSET", "h", "b", "two", "a", "one"}, ":2\r\n"},
		{[]string{"HSET", "h", "a", "ONE"}, ":0\r\n"},
		{[]string{"HGET", "h", "a"}, "$3\r\nONE\r\n"},
		{[]string{"HGET", "h", "missing"}, "$-1\r\n"},
		{[]string{"HLEN", "h"}, ":2\r\n"},
		{[]string{"HEXISTS", "h", "b"}, ":1\r\n"},
		{[]string{"HEXISTS", "h", "missing"}, ":0\r\n"},
		{[]string{"HMGET", "h", "b", "missing", "a"}, "*3\r\n$3\r\ntwo\r\n$-1\r\n$3\r\nONE\r\n"},
		{[]string{"HKEYS", "h"}, "*2\r\n$1\r\na\r\n$1\r\nb\r\n"},
		{[]string{"HVALS", "h"}, "*2\r\n$3\r\nONE\r\n$3\r\ntwo\r\n"},
		{[]string{"HGETALL", "h"}, "*4\r\n$1\r\na\r\n$3\r\nONE\r\n$1\r\nb\r\n$3\r\ntwo\r\n"},
		{[]string{"HSTRLEN", "h", "a"}, ":3\r\n"},
		{[]string{"HSTRLEN", "h", "missing"}, ":0\r\n"},
		{[]string{"HSETNX", "h", "a", "nope"}, ":0\r\n"},
		{[]string{"HSETNX", "h", "c", "three"}, ":1\r\n"},
		{[]string{"HDEL", "h", "a", "missing"}, ":1\r\n"},
		{[]string{"HLEN", "h"}, ":2\r\n"},
		{[]string{"HDEL", "h", "b", "c"}, ":2\r\n"},
		{[]string{"HLEN", "h"}, ":0\r\n"},
		{[]string{"HMGET", "missing", "a", "b"}, "*2\r\n$-1\r\n$-1\r\n"},
		{[]string{"HGETALL", "missing"}, "*0\r\n"},
	} {
		if got := execute(t, s, tc.args...); got != tc.want {
			t.Fatalf("%q got %q want %q", tc.args, got, tc.want)
		}
	}

	if !strings.Contains(execute(t, s, "COMMAND"), "hset") {
		t.Fatal("HASH commands missing from COMMAND metadata")
	}
}

func TestHashCommandsWrongTypeAndArity(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "plain", "value")

	for _, args := range [][]byte{
		{[]byte("HGET"), []byte("plain"), []byte("field")},
		{[]byte("HSET"), []byte("plain"), []byte("field"), []byte("value")},
		{[]byte("HDEL"), []byte("plain"), []byte("field")},
		{[]byte("HLEN"), []byte("plain")},
		{[]byte("HSETNX"), []byte("plain"), []byte("field"), []byte("value")},
	} {
		if _, err := s.Execute(args); err == nil || !strings.Contains(err.Error(), "WRONGTYPE") {
			t.Fatalf("%q error = %v, want WRONGTYPE", args, err)
		}
	}

	if _, err := s.Execute([][]byte{[]byte("HSET"), []byte("h"), []byte("field")}); err == nil {
		t.Fatal("HSET accepted unmatched field/value")
	}
}

func TestHashSetNXPreservesTTL(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "HSET", "h", "a", "1")
	execute(t, s, "PEXPIRE", "h", "60000")

	before := execute(t, s, "PTTL", "h")
	if before == ":-1\r\n" || before == ":-2\r\n" {
		t.Fatalf("expected TTL before HSETNX, got %q", before)
	}

	if got := execute(t, s, "HSETNX", "h", "b", "2"); got != ":1\r\n" {
		t.Fatal(got)
	}
	if got := execute(t, s, "PTTL", "h"); got == ":-1\r\n" || got == ":-2\r\n" {
		t.Fatalf("HSETNX lost TTL: %q", got)
	}
}
