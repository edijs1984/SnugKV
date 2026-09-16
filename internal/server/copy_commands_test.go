package server

import (
	"strconv"
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func executeCopyTest(t *testing.T, s *Server, args ...string) (string, error) {
	t.Helper()
	command := make([][]byte, len(args))
	for i := range args {
		command[i] = []byte(args[i])
	}
	response, err := s.Execute(command)
	return string(response), err
}

func TestCopyBasicReplaceAndOptions(t *testing.T) {
	s := New(engine.New())
	if _, err := executeCopyTest(t, s, "SET", "src", "hello"); err != nil {
		t.Fatal(err)
	}

	if got, err := executeCopyTest(t, s, "COPY", "src", "dst"); err != nil || got != ":1\r\n" {
		t.Fatalf("COPY = %q, %v", got, err)
	}
	if got, _ := executeCopyTest(t, s, "GET", "src"); got != "$5\r\nhello\r\n" {
		t.Fatalf("source changed: %q", got)
	}
	if got, _ := executeCopyTest(t, s, "GET", "dst"); got != "$5\r\nhello\r\n" {
		t.Fatalf("destination = %q", got)
	}

	if _, err := executeCopyTest(t, s, "SET", "occupied", "old"); err != nil {
		t.Fatal(err)
	}
	if got, err := executeCopyTest(t, s, "COPY", "src", "occupied"); err != nil || got != ":0\r\n" {
		t.Fatalf("COPY existing = %q, %v", got, err)
	}
	if got, _ := executeCopyTest(t, s, "GET", "occupied"); got != "$3\r\nold\r\n" {
		t.Fatalf("non-REPLACE destination changed: %q", got)
	}
	if got, err := executeCopyTest(t, s, "COPY", "src", "occupied", "REPLACE"); err != nil || got != ":1\r\n" {
		t.Fatalf("COPY REPLACE = %q, %v", got, err)
	}
	if got, _ := executeCopyTest(t, s, "GET", "occupied"); got != "$5\r\nhello\r\n" {
		t.Fatalf("REPLACE destination = %q", got)
	}

	if got, err := executeCopyTest(t, s, "COPY", "src", "dbzero", "DB", "0"); err != nil || got != ":1\r\n" {
		t.Fatalf("COPY DB 0 = %q, %v", got, err)
	}
	if _, err := executeCopyTest(t, s, "COPY", "src", "bad", "DB", "1"); err == nil || err.Error() != "ERR DB index is out of range" {
		t.Fatalf("COPY DB 1 error = %v", err)
	}
	if _, err := executeCopyTest(t, s, "COPY", "src", "bad", "DB", "nope"); err == nil || err.Error() != "ERR value is not an integer or out of range" {
		t.Fatalf("COPY invalid DB error = %v", err)
	}
	if _, err := executeCopyTest(t, s, "COPY", "src", "bad", "DB"); err == nil || err.Error() != "ERR syntax error" {
		t.Fatalf("COPY missing DB error = %v", err)
	}
	if _, err := executeCopyTest(t, s, "COPY", "src", "bad", "UNKNOWN"); err == nil || err.Error() != "ERR syntax error" {
		t.Fatalf("COPY unknown option error = %v", err)
	}
	if _, err := executeCopyTest(t, s, "COPY", "src", "src"); err == nil || err.Error() != "ERR source and destination objects are the same" {
		t.Fatalf("COPY same key error = %v", err)
	}
	if got, err := executeCopyTest(t, s, "COPY", "missing", "new"); err != nil || got != ":0\r\n" {
		t.Fatalf("COPY missing = %q, %v", got, err)
	}
}

func TestCopyPreservesTTLAndCreatesIndependentValue(t *testing.T) {
	s := New(engine.New())
	executeCopyTest(t, s, "RPUSH", "src:list", "a", "b")
	executeCopyTest(t, s, "PEXPIRE", "src:list", "60000")
	if got, err := executeCopyTest(t, s, "COPY", "src:list", "dst:list"); err != nil || got != ":1\r\n" {
		t.Fatalf("COPY list = %q, %v", got, err)
	}

	for _, key := range []string{"src:list", "dst:list"} {
		got, err := executeCopyTest(t, s, "PTTL", key)
		if err != nil || !strings.HasPrefix(got, ":") {
			t.Fatalf("PTTL %s = %q, %v", key, got, err)
		}
		ms, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(got, ":"), "\r\n"), 10, 64)
		if err != nil || ms <= 0 || ms > 60000 {
			t.Fatalf("PTTL %s = %q", key, got)
		}
	}

	executeCopyTest(t, s, "RPUSH", "src:list", "c")
	if got, _ := executeCopyTest(t, s, "LRANGE", "dst:list", "0", "-1"); got != "*2\r\n$1\r\na\r\n$1\r\nb\r\n" {
		t.Fatalf("destination changed with source mutation: %q", got)
	}
}

func TestCopyPreservesNativeTypes(t *testing.T) {
	s := New(engine.New())

	executeCopyTest(t, s, "HSET", "src:h", "field", "value")
	if got, err := executeCopyTest(t, s, "COPY", "src:h", "dst:h"); err != nil || got != ":1\r\n" {
		t.Fatalf("COPY hash = %q, %v", got, err)
	}
	if got, _ := executeCopyTest(t, s, "TYPE", "dst:h"); got != "+hash\r\n" {
		t.Fatalf("hash type = %q", got)
	}
	if got, _ := executeCopyTest(t, s, "HGET", "dst:h", "field"); got != "$5\r\nvalue\r\n" {
		t.Fatalf("hash value = %q", got)
	}

	executeCopyTest(t, s, "SADD", "src:s", "one", "two")
	executeCopyTest(t, s, "COPY", "src:s", "dst:s")
	if got, _ := executeCopyTest(t, s, "TYPE", "dst:s"); got != "+set\r\n" {
		t.Fatalf("set type = %q", got)
	}
	if got, _ := executeCopyTest(t, s, "SISMEMBER", "dst:s", "two"); got != ":1\r\n" {
		t.Fatalf("set value = %q", got)
	}

	executeCopyTest(t, s, "RPUSH", "src:l", "x", "y")
	executeCopyTest(t, s, "COPY", "src:l", "dst:l")
	if got, _ := executeCopyTest(t, s, "TYPE", "dst:l"); got != "+list\r\n" {
		t.Fatalf("list type = %q", got)
	}
	if got, _ := executeCopyTest(t, s, "LRANGE", "dst:l", "0", "-1"); got != "*2\r\n$1\r\nx\r\n$1\r\ny\r\n" {
		t.Fatalf("list value = %q", got)
	}

	executeCopyTest(t, s, "ZADD", "src:z", "1.5", "one", "2.5", "two")
	executeCopyTest(t, s, "COPY", "src:z", "dst:z")
	if got, _ := executeCopyTest(t, s, "TYPE", "dst:z"); got != "+zset\r\n" {
		t.Fatalf("zset type = %q", got)
	}
	if got, _ := executeCopyTest(t, s, "ZSCORE", "dst:z", "two"); got != "$3\r\n2.5\r\n" {
		t.Fatalf("zset value = %q", got)
	}

	executeCopyTest(t, s, "XADD", "src:x", "1-0", "field", "value")
	executeCopyTest(t, s, "COPY", "src:x", "dst:x")
	if got, _ := executeCopyTest(t, s, "TYPE", "dst:x"); got != "+stream\r\n" {
		t.Fatalf("stream type = %q", got)
	}
	if got, _ := executeCopyTest(t, s, "XLEN", "dst:x"); got != ":1\r\n" {
		t.Fatalf("stream length = %q", got)
	}
}

func TestCopyReplaceOverwritesDifferentType(t *testing.T) {
	s := New(engine.New())
	executeCopyTest(t, s, "SADD", "src", "a", "b")
	executeCopyTest(t, s, "SET", "dst", "string")
	if got, err := executeCopyTest(t, s, "COPY", "src", "dst", "REPLACE"); err != nil || got != ":1\r\n" {
		t.Fatalf("COPY REPLACE = %q, %v", got, err)
	}
	if got, _ := executeCopyTest(t, s, "TYPE", "dst"); got != "+set\r\n" {
		t.Fatalf("destination type = %q", got)
	}
}
