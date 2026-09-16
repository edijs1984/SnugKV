package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestEvalBasicRepliesKeysAndArgv(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "EVAL", "return 10", "0"); got != ":10\r\n" {
		t.Fatalf("integer EVAL = %q", got)
	}
	if got := execute(t, s, "EVAL", "return 3.9", "0"); got != ":3\r\n" {
		t.Fatalf("float EVAL = %q", got)
	}
	if got := execute(t, s, "EVAL", "return false", "0"); got != "$-1\r\n" {
		t.Fatalf("false EVAL = %q", got)
	}
	if got := execute(t, s, "EVAL", "return true", "0"); got != ":1\r\n" {
		t.Fatalf("true EVAL = %q", got)
	}

	got := execute(t, s, "EVAL", "return {KEYS[1],ARGV[1],false,true,3.9}", "1", "my-key", "my-arg")
	want := "*5\r\n$6\r\nmy-key\r\n$6\r\nmy-arg\r\n$-1\r\n:1\r\n:3\r\n"
	if got != want {
		t.Fatalf("KEYS/ARGV EVAL = %q, want %q", got, want)
	}
}

func TestEvalRedisCallAndPCall(t *testing.T) {
	s := New(engine.New())

	script := "redis.call('SET',KEYS[1],ARGV[1]); return redis.call('GET',KEYS[1])"
	if got := execute(t, s, "EVAL", script, "1", "lua:key", "hello"); got != "$5\r\nhello\r\n" {
		t.Fatalf("redis.call SET/GET = %q", got)
	}
	if got := execute(t, s, "GET", "lua:key"); got != "$5\r\nhello\r\n" {
		t.Fatalf("GET after EVAL = %q", got)
	}

	pcallScript := "local r=redis.pcall('SET','only-key'); return r.err"
	got := execute(t, s, "EVAL", pcallScript, "0")
	if !strings.Contains(got, "wrong number of arguments") {
		t.Fatalf("redis.pcall error = %q", got)
	}

	status := execute(t, s, "EVAL", "return redis.status_reply('READY')", "0")
	if status != "+READY\r\n" {
		t.Fatalf("status reply = %q", status)
	}

	sha := execute(t, s, "EVAL", "return redis.sha1hex('abc')", "0")
	if sha != "$40\r\na9993e364706816aba3e25717850c26c9cd0d89d\r\n" {
		t.Fatalf("sha1hex = %q", sha)
	}
}

func TestEvalRuntimeErrorKeepsEarlierWrites(t *testing.T) {
	s := New(engine.New())
	script := "redis.call('SET',KEYS[1],'written'); error('boom')"
	args := [][]byte{[]byte("EVAL"), []byte(script), []byte("1"), []byte("lua:partial")}
	if _, err := s.Execute(args); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("runtime error = %v", err)
	}
	if got := execute(t, s, "GET", "lua:partial"); got != "$7\r\nwritten\r\n" {
		t.Fatalf("write before runtime error was lost: %q", got)
	}
}

func TestEvalRejectsUnsafeNestedCommands(t *testing.T) {
	s := New(engine.New())
	for _, script := range []string{
		"return redis.call('EVAL','return 1',0)",
		"return redis.call('MULTI')",
		"return redis.call('BLPOP','queue',1)",
		"return redis.call('SNUG.STATS')",
	} {
		args := [][]byte{[]byte("EVAL"), []byte(script), []byte("0")}
		if _, err := s.Execute(args); err == nil || !strings.Contains(err.Error(), "not allowed from script") {
			t.Fatalf("script %q error = %v", script, err)
		}
	}
}

func TestScriptLoadExistsEvalSHAAndFlush(t *testing.T) {
	s := New(engine.New())
	const source = "return 'Immabe a cached script'"
	const expectedSHA = "c664a3bf70bd1d45c4284ffebb65a6f2299bfc9f"

	if got := execute(t, s, "SCRIPT", "LOAD", source); got != "$40\r\n"+expectedSHA+"\r\n" {
		t.Fatalf("SCRIPT LOAD = %q", got)
	}
	if got := execute(t, s, "SCRIPT", "EXISTS", expectedSHA, strings.Repeat("0", 40)); got != "*2\r\n:1\r\n:0\r\n" {
		t.Fatalf("SCRIPT EXISTS = %q", got)
	}
	if got := execute(t, s, "EVALSHA", expectedSHA, "0"); got != "$25\r\nImmabe a cached script\r\n" {
		t.Fatalf("EVALSHA = %q", got)
	}
	if got := execute(t, s, "SCRIPT", "FLUSH", "ASYNC"); got != "+OK\r\n" {
		t.Fatalf("SCRIPT FLUSH = %q", got)
	}
	if got := execute(t, s, "SCRIPT", "EXISTS", expectedSHA); got != "*1\r\n:0\r\n" {
		t.Fatalf("SCRIPT EXISTS after FLUSH = %q", got)
	}

	args := [][]byte{[]byte("EVALSHA"), []byte(expectedSHA), []byte("0")}
	if _, err := s.Execute(args); err == nil || !strings.HasPrefix(err.Error(), "NOSCRIPT ") {
		t.Fatalf("EVALSHA missing script error = %v", err)
	}
}

func TestEvalCachesSuccessfulScript(t *testing.T) {
	s := New(engine.New())
	const source = "return ARGV[1]"
	sha := scriptSHA(source)

	if got := execute(t, s, "EVAL", source, "0", "cached"); got != "$6\r\ncached\r\n" {
		t.Fatalf("EVAL = %q", got)
	}
	if got := execute(t, s, "SCRIPT", "EXISTS", sha); got != "*1\r\n:1\r\n" {
		t.Fatalf("SCRIPT EXISTS after EVAL = %q", got)
	}
	if got := execute(t, s, "EVALSHA", sha, "0", "again"); got != "$5\r\nagain\r\n" {
		t.Fatalf("EVALSHA after EVAL = %q", got)
	}
}

func TestEvalArgumentAndCompileErrors(t *testing.T) {
	s := New(engine.New())

	bad := [][][]byte{
		{[]byte("EVAL"), []byte("return 1"), []byte("-1")},
		{[]byte("EVAL"), []byte("return 1"), []byte("2"), []byte("one")},
		{[]byte("EVAL"), []byte("return 1"), []byte("not-a-number")},
	}
	for _, args := range bad {
		if _, err := s.Execute(args); err == nil {
			t.Fatalf("expected argument error for %q", args)
		}
	}

	if _, err := s.Execute([][]byte{[]byte("SCRIPT"), []byte("LOAD"), []byte("return function(")}); err == nil || !strings.Contains(err.Error(), "compiling script") {
		t.Fatalf("SCRIPT LOAD compile error = %v", err)
	}
	if _, err := s.Execute([][]byte{[]byte("EVAL"), []byte("return function("), []byte("0")}); err == nil || !strings.Contains(err.Error(), "compiling script") {
		t.Fatalf("EVAL compile error = %v", err)
	}
}
