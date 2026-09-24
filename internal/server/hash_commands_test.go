package server

import (
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestHashFieldExpirationCommands(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "HSET", "hfe", "a", "1", "b", "2", "c", "3")

	got := execute(t, s, "HPEXPIRE", "hfe", "60000", "FIELDS", "2", "a", "missing")
	if got != "*2\r\n:1\r\n:-2\r\n" {
		t.Fatalf("HPEXPIRE = %q", got)
	}

	got = execute(t, s, "HPTTL", "hfe", "FIELDS", "3", "a", "b", "missing")
	if !strings.HasPrefix(got, "*3\r\n:") || !strings.Contains(got, "\r\n:-1\r\n:-2\r\n") {
		t.Fatalf("HPTTL = %q", got)
	}

	got = execute(t, s, "HPERSIST", "hfe", "FIELDS", "3", "a", "b", "missing")
	if got != "*3\r\n:1\r\n:-1\r\n:-2\r\n" {
		t.Fatalf("HPERSIST = %q", got)
	}

	got = execute(t, s, "HPTTL", "hfe", "FIELDS", "1", "a")
	if got != "*1\r\n:-1\r\n" {
		t.Fatalf("HPTTL after HPERSIST = %q", got)
	}

	past := time.Now().Add(-time.Second).UnixMilli()
	got = execute(t, s, "HPEXPIREAT", "hfe", strconv.FormatInt(past, 10), "FIELDS", "1", "c")
	if got != "*1\r\n:2\r\n" {
		t.Fatalf("HPEXPIREAT past = %q", got)
	}
	if got = execute(t, s, "HEXISTS", "hfe", "c"); got != ":0\r\n" {
		t.Fatalf("expired field exists = %q", got)
	}

	future := time.Now().Add(90 * time.Second).UnixMilli()
	got = execute(t, s, "HPEXPIREAT", "hfe", strconv.FormatInt(future, 10), "FIELDS", "1", "a")
	if got != "*1\r\n:1\r\n" {
		t.Fatalf("HPEXPIREAT future = %q", got)
	}

	got = execute(t, s, "HPEXPIRETIME", "hfe", "FIELDS", "2", "a", "b")
	if !strings.HasPrefix(got, "*2\r\n:") || !strings.HasSuffix(got, "\r\n:-1\r\n") {
		t.Fatalf("HPEXPIRETIME = %q", got)
	}

	got = execute(t, s, "HTTL", "hfe", "FIELDS", "2", "a", "b")
	if !strings.HasPrefix(got, "*2\r\n:") || !strings.HasSuffix(got, "\r\n:-1\r\n") {
		t.Fatalf("HTTL = %q", got)
	}
}

func TestHashFieldExpirationConditions(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "HSET", "cond", "a", "1", "b", "2")

	base := time.Now().Add(2 * time.Minute).UnixMilli()
	if got := execute(t, s, "HPEXPIREAT", "cond", strconv.FormatInt(base, 10), "NX", "FIELDS", "2", "a", "b"); got != "*2\r\n:1\r\n:1\r\n" {
		t.Fatalf("NX initial = %q", got)
	}
	if got := execute(t, s, "HPEXPIREAT", "cond", strconv.FormatInt(base+1000, 10), "NX", "FIELDS", "1", "a"); got != "*1\r\n:0\r\n" {
		t.Fatalf("NX existing = %q", got)
	}

	execute(t, s, "HSET", "cond", "c", "3")
	if got := execute(t, s, "HPEXPIREAT", "cond", strconv.FormatInt(base+1000, 10), "XX", "FIELDS", "2", "a", "c"); got != "*2\r\n:1\r\n:0\r\n" {
		t.Fatalf("XX = %q", got)
	}

	if got := execute(t, s, "HPEXPIREAT", "cond", strconv.FormatInt(base+500, 10), "GT", "FIELDS", "2", "a", "c"); got != "*2\r\n:0\r\n:0\r\n" {
		t.Fatalf("GT lower/no-ttl = %q", got)
	}
	if got := execute(t, s, "HPEXPIREAT", "cond", strconv.FormatInt(base+2000, 10), "GT", "FIELDS", "1", "a"); got != "*1\r\n:1\r\n" {
		t.Fatalf("GT higher = %q", got)
	}

	if got := execute(t, s, "HPEXPIREAT", "cond", strconv.FormatInt(base+3000, 10), "LT", "FIELDS", "2", "a", "c"); got != "*2\r\n:0\r\n:1\r\n" {
		t.Fatalf("LT higher/no-ttl = %q", got)
	}
	if got := execute(t, s, "HPEXPIREAT", "cond", strconv.FormatInt(base+1500, 10), "LT", "FIELDS", "1", "a"); got != "*1\r\n:1\r\n" {
		t.Fatalf("LT lower = %q", got)
	}
}

func TestHashFieldExpirationSyntax(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "HSET", "h", "a", "1")

	for _, args := range [][]string{
		{"HPTTL", "h", "NOPE", "1", "a"},
		{"HPTTL", "h", "FIELDS", "0"},
		{"HPTTL", "h", "FIELDS", "2", "a"},
	} {
		raw := make([][]byte, len(args))
		for i := range args {
			raw[i] = []byte(args[i])
		}
		if _, err := s.Execute(raw); err == nil {
			t.Fatalf("%v unexpectedly succeeded", args)
		}
	}
}

func TestHashCommandsWrongTypeAndArity(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "plain", "value")

	for _, args := range [][][]byte{
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
