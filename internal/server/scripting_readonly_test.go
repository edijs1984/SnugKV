package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestEvalROReadsKeysAndArgv(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "ro:key", "value")

	got := execute(t, s, "EVAL_RO", "return {redis.call('GET',KEYS[1]),ARGV[1]}", "1", "ro:key", "arg")
	want := "*2\r\n$5\r\nvalue\r\n$3\r\narg\r\n"
	if got != want {
		t.Fatalf("EVAL_RO = %q, want %q", got, want)
	}
}

func TestEvalRORejectsWriteCommands(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "ro:key", "before")

	args := [][]byte{
		[]byte("EVAL_RO"),
		[]byte("return redis.call('SET',KEYS[1],'after')"),
		[]byte("1"),
		[]byte("ro:key"),
	}
	_, err := s.Execute(args)
	if err == nil || !strings.Contains(err.Error(), "ERR Write commands are not allowed from read-only scripts.") {
		t.Fatalf("EVAL_RO SET error = %v", err)
	}
	if got := execute(t, s, "GET", "ro:key"); got != "$6\r\nbefore\r\n" {
		t.Fatalf("read-only script mutated key: %q", got)
	}
}

func TestEvalROPCallReturnsWriteError(t *testing.T) {
	s := New(engine.New())
	got := execute(t, s, "EVAL_RO", "local r=redis.pcall('SET','ro:key','value'); return r.err", "0")
	want := "$57\r\nERR Write commands are not allowed from read-only scripts.\r\n"
	if got != want {
		t.Fatalf("EVAL_RO redis.pcall write error = %q, want %q", got, want)
	}
	if got := execute(t, s, "EXISTS", "ro:key"); got != ":0\r\n" {
		t.Fatalf("pcall write changed key: %q", got)
	}
}

func TestEvalRORejectsPublishSideEffects(t *testing.T) {
	s := New(engine.New())
	for _, command := range []string{"PUBLISH", "SPUBLISH"} {
		script := "local r=redis.pcall('" + command + "','channel','message'); return r.err"
		got := execute(t, s, "EVAL_RO", script, "0")
		want := "$57\r\nERR Write commands are not allowed from read-only scripts.\r\n"
		if got != want {
			t.Fatalf("%s from EVAL_RO = %q, want %q", command, got, want)
		}
	}
}

func TestEvalSHAROSharesScriptCache(t *testing.T) {
	s := New(engine.New())
	const source = "return redis.call('GET',KEYS[1])"
	sha := scriptSHA(source)

	execute(t, s, "SET", "ro:key", "cached")
	if got := execute(t, s, "SCRIPT", "LOAD", source); got != "$40\r\n"+sha+"\r\n" {
		t.Fatalf("SCRIPT LOAD = %q", got)
	}
	if got := execute(t, s, "EVALSHA_RO", strings.ToUpper(sha), "1", "ro:key"); got != "$6\r\ncached\r\n" {
		t.Fatalf("EVALSHA_RO = %q", got)
	}

	execute(t, s, "SCRIPT", "FLUSH")
	args := [][]byte{[]byte("EVALSHA_RO"), []byte(sha), []byte("0")}
	if _, err := s.Execute(args); err == nil || !strings.HasPrefix(err.Error(), "NOSCRIPT ") {
		t.Fatalf("EVALSHA_RO missing script error = %v", err)
	}
}

func TestEvalROCachesSuccessfulScript(t *testing.T) {
	s := New(engine.New())
	const source = "return ARGV[1]"
	sha := scriptSHA(source)

	if got := execute(t, s, "EVAL_RO", source, "0", "cached"); got != "$6\r\ncached\r\n" {
		t.Fatalf("EVAL_RO = %q", got)
	}
	if got := execute(t, s, "SCRIPT", "EXISTS", sha); got != "*1\r\n:1\r\n" {
		t.Fatalf("SCRIPT EXISTS after EVAL_RO = %q", got)
	}
	if got := execute(t, s, "EVALSHA_RO", sha, "0", "again"); got != "$5\r\nagain\r\n" {
		t.Fatalf("EVALSHA_RO after EVAL_RO = %q", got)
	}
}

func TestEvalRONestedScriptingIsRejected(t *testing.T) {
	s := New(engine.New())

	writableOuter := [][]byte{
		[]byte("EVAL"),
		[]byte("return redis.call('EVAL_RO','return 1',0)"),
		[]byte("0"),
	}
	if _, err := s.Execute(writableOuter); err == nil || !strings.Contains(err.Error(), "not allowed from script") {
		t.Fatalf("EVAL -> EVAL_RO nested error = %v", err)
	}

	readOnlyOuter := [][]byte{
		[]byte("EVAL_RO"),
		[]byte("return redis.call('EVALSHA_RO','deadbeef',0)"),
		[]byte("0"),
	}
	if _, err := s.Execute(readOnlyOuter); err == nil || !strings.Contains(err.Error(), "not allowed from script") {
		t.Fatalf("EVAL_RO -> EVALSHA_RO nested error = %v", err)
	}
}

func TestEvalROWritableEvalRemainsWritable(t *testing.T) {
	s := New(engine.New())
	if got := execute(t, s, "EVAL", "return redis.call('SET',KEYS[1],'ok')", "1", "rw:key"); got != "+OK\r\n" {
		t.Fatalf("EVAL SET = %q", got)
	}
	if got := execute(t, s, "GET", "rw:key"); got != "$2\r\nok\r\n" {
		t.Fatalf("GET after writable EVAL = %q", got)
	}
}
