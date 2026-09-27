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


func TestHGetDelRedis8Semantics(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "HSET", "h", "a", "1", "b", "2", "c", "3", "e", "5")

	got := execute(t, s, "HGETDEL", "h", "FIELDS", "4", "c", "a", "c", "e")
	want := "*4\r\n$1\r\n3\r\n$1\r\n1\r\n$-1\r\n$1\r\n5\r\n"
	if got != want {
		t.Fatalf("HGETDEL = %q want %q", got, want)
	}

	if got := execute(t, s, "HGETALL", "h"); got != "*2\r\n$1\r\nb\r\n$1\r\n2\r\n" {
		t.Fatalf("HGETALL after HGETDEL = %q", got)
	}

	if got := execute(t, s, "HGETDEL", "missing", "FIELDS", "2", "a", "b"); got != "*2\r\n$-1\r\n$-1\r\n" {
		t.Fatalf("HGETDEL missing = %q", got)
	}
}

func TestHGetExRedis8Semantics(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "HSET", "h", "a", "1", "b", "2")
	execute(t, s, "HPEXPIRE", "h", "60000", "FIELDS", "1", "a")

	got := execute(t, s, "HGETEX", "h", "PERSIST", "FIELDS", "2", "a", "b")
	if got != "*2\r\n$1\r\n1\r\n$1\r\n2\r\n" {
		t.Fatalf("HGETEX PERSIST = %q", got)
	}
	if got := execute(t, s, "HPTTL", "h", "FIELDS", "1", "a"); got != "*1\r\n:-1\r\n" {
		t.Fatalf("HPTTL after HGETEX PERSIST = %q", got)
	}

	got = execute(t, s, "HGETEX", "h", "PX", "60000", "FIELDS", "2", "a", "missing")
	if got != "*2\r\n$1\r\n1\r\n$-1\r\n" {
		t.Fatalf("HGETEX PX = %q", got)
	}
	ttl := execute(t, s, "HPTTL", "h", "FIELDS", "1", "a")
	if ttl == "*1\r\n:-1\r\n" || ttl == "*1\r\n:-2\r\n" {
		t.Fatalf("expected HGETEX PX to set TTL, got %q", ttl)
	}

	past := strconv.FormatInt(time.Now().Add(-time.Second).UnixMilli(), 10)
	got = execute(t, s, "HGETEX", "h", "PXAT", past, "FIELDS", "1", "b")
	if got != "*1\r\n$1\r\n2\r\n" {
		t.Fatalf("HGETEX past PXAT = %q", got)
	}
	if got := execute(t, s, "HEXISTS", "h", "b"); got != ":0\r\n" {
		t.Fatalf("field should be deleted by past HGETEX PXAT, got %q", got)
	}
}

func TestHSetExRedis8Semantics(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "HSETEX", "h", "FIELDS", "2", "a", "1", "b", "2"); got != ":1\r\n" {
		t.Fatalf("HSETEX basic = %q", got)
	}
	if got := execute(t, s, "HMGET", "h", "a", "b"); got != "*2\r\n$1\r\n1\r\n$1\r\n2\r\n" {
		t.Fatalf("HMGET after HSETEX = %q", got)
	}

	if got := execute(t, s, "HSETEX", "h", "FNX", "FIELDS", "2", "c", "3", "a", "x"); got != ":0\r\n" {
		t.Fatalf("HSETEX FNX = %q", got)
	}
	if got := execute(t, s, "HEXISTS", "h", "c"); got != ":0\r\n" {
		t.Fatalf("FNX must be all-or-nothing, c exists: %q", got)
	}
	if got := execute(t, s, "HGET", "h", "a"); got != "$1\r\n1\r\n" {
		t.Fatalf("FNX changed existing field: %q", got)
	}

	if got := execute(t, s, "HSETEX", "h", "FXX", "FIELDS", "2", "a", "A", "missing", "x"); got != ":0\r\n" {
		t.Fatalf("HSETEX FXX miss = %q", got)
	}
	if got := execute(t, s, "HGET", "h", "a"); got != "$1\r\n1\r\n" {
		t.Fatalf("FXX partial update occurred: %q", got)
	}

	if got := execute(t, s, "HSETEX", "h", "PX", "60000", "FIELDS", "1", "a", "ttl"); got != ":1\r\n" {
		t.Fatalf("HSETEX PX = %q", got)
	}
	before := execute(t, s, "HPEXPIRETIME", "h", "FIELDS", "1", "a")
	if strings.Contains(before, ":-1\r\n") || strings.Contains(before, ":-2\r\n") {
		t.Fatalf("expected expiry after HSETEX PX, got %q", before)
	}

	if got := execute(t, s, "HSETEX", "h", "KEEPTTL", "FIELDS", "1", "a", "kept"); got != ":1\r\n" {
		t.Fatalf("HSETEX KEEPTTL = %q", got)
	}
	after := execute(t, s, "HPEXPIRETIME", "h", "FIELDS", "1", "a")
	if after != before {
		t.Fatalf("KEEPTTL changed expiry: before=%q after=%q", before, after)
	}

	if got := execute(t, s, "HSETEX", "h", "FIELDS", "1", "a", "clear"); got != ":1\r\n" {
		t.Fatalf("HSETEX clear TTL = %q", got)
	}
	if got := execute(t, s, "HPTTL", "h", "FIELDS", "1", "a"); got != "*1\r\n:-1\r\n" {
		t.Fatalf("ordinary HSETEX should clear TTL, got %q", got)
	}
}

func TestHashRedis8ExCommandErrors(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "HGETDEL", "h", "BAD", "1", "a"); !strings.Contains(got, "Mandatory argument FIELDS") {
		t.Fatalf("HGETDEL FIELDS error = %q", got)
	}
	if got := execute(t, s, "HGETEX", "h", "EX", "10", "BAD", "1", "a"); !strings.Contains(got, "Mandatory argument FIELDS") {
		t.Fatalf("HGETEX FIELDS error = %q", got)
	}
	if got := execute(t, s, "HSETEX", "h", "FNX", "FXX", "FIELDS", "1", "a", "1"); !strings.Contains(got, "Only one of FXX or FNX") {
		t.Fatalf("HSETEX condition error = %q", got)
	}
	if got := execute(t, s, "HSETEX", "h", "EX", "10", "KEEPTTL", "FIELDS", "1", "a", "1"); !strings.Contains(got, "Only one of EX, PX, EXAT, PXAT or KEEPTTL") {
		t.Fatalf("HSETEX expiration error = %q", got)
	}
}
