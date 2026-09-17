package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func functionDumpBytes(t *testing.T, s *Server) []byte {
	t.Helper()
	response, err := s.Execute([][]byte{[]byte("FUNCTION"), []byte("DUMP")})
	if err != nil {
		t.Fatalf("FUNCTION DUMP error: %v", err)
	}
	if len(response) < 7 || response[0] != '$' {
		t.Fatalf("FUNCTION DUMP response = %q", response)
	}
	lineEnd := strings.Index(string(response), "\r\n")
	if lineEnd < 0 {
		t.Fatalf("invalid bulk response: %q", response)
	}
	payloadStart := lineEnd + 2
	return append([]byte(nil), response[payloadStart:len(response)-2]...)
}

func TestFunctionDumpRestoreRoundTrip(t *testing.T) {
	s := New(engine.New())
	code := "#!lua name=dumpme\nredis.register_function('hello', function(keys,args) return args[1] end)"
	loadFunctionLibrary(t, s, code)
	payload := functionDumpBytes(t, s)

	if got := execute(t, s, "FUNCTION", "FLUSH"); got != "+OK\r\n" {
		t.Fatalf("FUNCTION FLUSH = %q", got)
	}
	if _, err := s.Execute([][]byte{[]byte("FUNCTION"), []byte("RESTORE"), payload}); err != nil {
		t.Fatalf("FUNCTION RESTORE error: %v", err)
	}
	if got := execute(t, s, "FCALL", "hello", "0", "world"); got != "$5\r\nworld\r\n" {
		t.Fatalf("restored FCALL = %q", got)
	}
}

func TestFunctionRestorePolicies(t *testing.T) {
	s := New(engine.New())
	loadFunctionLibrary(t, s, "#!lua name=one\nredis.register_function('one_fn', function() return 1 end)")
	payload := functionDumpBytes(t, s)

	if _, err := s.Execute([][]byte{[]byte("FUNCTION"), []byte("RESTORE"), payload}); err == nil {
		t.Fatal("APPEND collision unexpectedly succeeded")
	}
	if got := execute(t, s, "FUNCTION", "RESTORE", string(payload), "REPLACE"); got != "+OK\r\n" {
		t.Fatalf("REPLACE restore = %q", got)
	}

	loadFunctionLibrary(t, s, "#!lua name=two\nredis.register_function('two_fn', function() return 2 end)")
	if got := execute(t, s, "FUNCTION", "RESTORE", string(payload), "FLUSH"); got != "+OK\r\n" {
		t.Fatalf("FLUSH restore = %q", got)
	}
	if _, err := s.Execute([][]byte{[]byte("FCALL"), []byte("two_fn"), []byte("0")}); err == nil || err.Error() != "ERR Function not found" {
		t.Fatalf("two_fn after FLUSH restore = %v", err)
	}
	if got := execute(t, s, "FCALL", "one_fn", "0"); got != ":1\r\n" {
		t.Fatalf("one_fn after FLUSH restore = %q", got)
	}
}

func TestFunctionRestoreRejectsCorruptPayloadAtomically(t *testing.T) {
	s := New(engine.New())
	loadFunctionLibrary(t, s, "#!lua name=keep\nredis.register_function('keep_fn', function() return 'ok' end)")
	payload := functionDumpBytes(t, s)
	payload[len(payload)/2] ^= 0x7f

	if _, err := s.Execute([][]byte{[]byte("FUNCTION"), []byte("RESTORE"), payload, []byte("FLUSH")}); err == nil || !strings.Contains(err.Error(), "payload version or checksum") {
		t.Fatalf("corrupt restore error = %v", err)
	}
	if got := execute(t, s, "FCALL", "keep_fn", "0"); got != "$2\r\nok\r\n" {
		t.Fatalf("registry changed after corrupt restore: %q", got)
	}
}

func TestFunctionRestoreRejectsBadPolicy(t *testing.T) {
	s := New(engine.New())
	payload := functionDumpBytes(t, s)
	if _, err := s.Execute([][]byte{[]byte("FUNCTION"), []byte("RESTORE"), payload, []byte("NOPE")}); err == nil || err.Error() != "ERR Wrong restore policy given, value should be either FLUSH, APPEND or REPLACE." {
		t.Fatalf("bad restore policy error = %v", err)
	}
}
