package server

import (
	"encoding/hex"
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


func TestFunctionDumpEmptyMatchesRedis82(t *testing.T) {
	payload, err := encodeFunctionDump(nil)
	if err != nil {
		t.Fatalf("encode empty FUNCTION DUMP: %v", err)
	}
	const redis82EmptyHex = "0c0096ed6880f5553c93"
	if got := hex.EncodeToString(payload); got != redis82EmptyHex {
		t.Fatalf("empty FUNCTION DUMP = %s, want %s", got, redis82EmptyHex)
	}
}

func TestFunctionRestoreDecodesRedis82LZFPayload(t *testing.T) {
	const redis82OneLibraryHex = "f5c3409a40b01f23216c7561206e616d653d6c69625f610a72656469732e72656769737465725f0b66756e6374696f6e7b0a2020c00b005f602e08276563686f5f61272c20190863616c6c6261636b3dc02212286b6579732c2061726773292072657475726e600c065b315d20656e6440330664657363726970405c063d27616c706861e00060605315666c6167733d7b276e6f2d777269746573277d0a7d0a0c004a7f552aa11ac9cc"
	payload, err := hex.DecodeString(redis82OneLibraryHex)
	if err != nil {
		t.Fatal(err)
	}
	codes, err := decodeFunctionDump(payload)
	if err != nil {
		t.Fatalf("decode Redis 8.2 FUNCTION DUMP: %v", err)
	}
	const want = "#!lua name=lib_a\nredis.register_function{\n  function_name='echo_a',\n  callback=function(keys, args) return args[1] end,\n  description='alpha function',\n  flags={'no-writes'}\n}\n"
	if len(codes) != 1 || codes[0] != want {
		t.Fatalf("decoded libraries = %#v", codes)
	}
}

func TestFunctionRestoreRedis82PayloadExecutes(t *testing.T) {
	s := New(engine.New())
	const redis82OneLibraryHex = "f5c3409a40b01f23216c7561206e616d653d6c69625f610a72656469732e72656769737465725f0b66756e6374696f6e7b0a2020c00b005f602e08276563686f5f61272c20190863616c6c6261636b3dc02212286b6579732c2061726773292072657475726e600c065b315d20656e6440330664657363726970405c063d27616c706861e00060605315666c6167733d7b276e6f2d777269746573277d0a7d0a0c004a7f552aa11ac9cc"
	payload, _ := hex.DecodeString(redis82OneLibraryHex)
	if _, err := s.Execute([][]byte{[]byte("FUNCTION"), []byte("RESTORE"), payload}); err != nil {
		t.Fatalf("restore Redis 8.2 payload: %v", err)
	}
	if got := execute(t, s, "FCALL_RO", "echo_a", "0", "hello"); got != "$5\r\nhello\r\n" {
		t.Fatalf("restored Redis function result = %q", got)
	}
}
