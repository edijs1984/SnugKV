package server

import (
	"snugkv/internal/engine"
	"strconv"
	"strings"
	"testing"
)

func hllArgs(values ...string) [][]byte {
	out := make([][]byte, len(values))
	for i, value := range values {
		out[i] = []byte(value)
	}
	return out
}

func TestHyperLogLogCommandsAndRedisStringType(t *testing.T) {
	s := New(engine.New())

	response, err := s.Execute(hllArgs("PFADD", "h"))
	if err != nil || string(response) != ":1\r\n" {
		t.Fatalf("empty PFADD = %q, err=%v", response, err)
	}
	response, err = s.Execute(hllArgs("TYPE", "h"))
	if err != nil || string(response) != "+string\r\n" {
		t.Fatalf("TYPE = %q, err=%v", response, err)
	}
	response, err = s.Execute(hllArgs("PFCOUNT", "h"))
	if err != nil || string(response) != ":0\r\n" {
		t.Fatalf("empty PFCOUNT = %q, err=%v", response, err)
	}

	response, err = s.Execute(hllArgs("PFADD", "h", "a", "b", "c"))
	if err != nil || string(response) != ":1\r\n" {
		t.Fatalf("PFADD values = %q, err=%v", response, err)
	}
	response, err = s.Execute(hllArgs("PFADD", "h", "a", "b"))
	if err != nil || string(response) != ":0\r\n" {
		t.Fatalf("duplicate PFADD = %q, err=%v", response, err)
	}
	response, err = s.Execute(hllArgs("PFCOUNT", "h"))
	if err != nil || string(response) != ":3\r\n" {
		t.Fatalf("PFCOUNT = %q, err=%v", response, err)
	}

	value, ok := s.store.Get("h")
	if !ok || len(value) < 18 || string(value[:4]) != "HYLL" {
		t.Fatalf("stored HLL bytes missing or invalid: len=%d", len(value))
	}
	if err := s.store.Set("copy", value, 0); err != nil {
		t.Fatal(err)
	}
	response, err = s.Execute(hllArgs("PFCOUNT", "copy"))
	if err != nil || string(response) != ":3\r\n" {
		t.Fatalf("GET/SET restored HLL PFCOUNT = %q, err=%v", response, err)
	}
}

func TestHyperLogLogMergeUnionAndTTL(t *testing.T) {
	s := New(engine.New())
	_, _ = s.Execute(hllArgs("PFADD", "a", "one", "two", "three"))
	_, _ = s.Execute(hllArgs("PFADD", "b", "three", "four", "five"))

	response, err := s.Execute(hllArgs("PEXPIRE", "a", "60000"))
	if err != nil || string(response) != ":1\r\n" {
		t.Fatalf("PEXPIRE = %q, err=%v", response, err)
	}
	before := s.store.TTL("a", true)
	if before <= 0 {
		t.Fatalf("PTTL before merge = %d", before)
	}

	response, err = s.Execute(hllArgs("PFMERGE", "a", "b", "missing"))
	if err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("PFMERGE = %q, err=%v", response, err)
	}
	after := s.store.TTL("a", true)
	if after <= 0 || after > before {
		t.Fatalf("TTL not preserved: before=%d after=%d", before, after)
	}
	response, err = s.Execute(hllArgs("PFCOUNT", "a"))
	if err != nil || string(response) != ":5\r\n" {
		t.Fatalf("merged PFCOUNT = %q, err=%v", response, err)
	}
	response, err = s.Execute(hllArgs("PFCOUNT", "a", "b", "missing"))
	if err != nil || string(response) != ":5\r\n" {
		t.Fatalf("union PFCOUNT = %q, err=%v", response, err)
	}

	response, err = s.Execute(hllArgs("PFMERGE", "empty"))
	if err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("empty PFMERGE = %q, err=%v", response, err)
	}
	response, err = s.Execute(hllArgs("PFCOUNT", "empty"))
	if err != nil || string(response) != ":0\r\n" {
		t.Fatalf("empty merged PFCOUNT = %q, err=%v", response, err)
	}
}

func TestHyperLogLogInvalidValueAndWrongType(t *testing.T) {
	s := New(engine.New())
	_, _ = s.Execute(hllArgs("SET", "plain", "hello"))
	if _, err := s.Execute(hllArgs("PFCOUNT", "plain")); err == nil || !strings.Contains(err.Error(), "not a valid HyperLogLog") {
		t.Fatalf("plain scalar PFCOUNT error = %v", err)
	}

	_, _ = s.Execute(hllArgs("HSET", "hash", "field", "value"))
	if _, err := s.Execute(hllArgs("PFCOUNT", "hash")); err == nil || !strings.Contains(err.Error(), "wrong kind of value") {
		t.Fatalf("native container PFCOUNT error = %v", err)
	}

	corrupt := make([]byte, 16)
	copy(corrupt[:4], "HYLL")
	corrupt[4] = 1
	corrupt[15] = 0x80
	if err := s.store.Set("corrupt", corrupt, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(hllArgs("PFCOUNT", "corrupt")); err == nil || !strings.HasPrefix(err.Error(), "INVALIDOBJ ") {
		t.Fatalf("corrupt sparse PFCOUNT error = %v", err)
	}
	if got := string(errorResponse(errHLLForTest())); got != "-INVALIDOBJ Corrupted HLL object detected\r\n" {
		t.Fatalf("INVALIDOBJ wire response = %q", got)
	}
}

// Keep the wire-class regression independent from unexported engine errors.
func errHLLForTest() error { return hllTestError("INVALIDOBJ Corrupted HLL object detected") }
type hllTestError string
func (e hllTestError) Error() string { return string(e) }

func TestHyperLogLogLargeEstimate(t *testing.T) {
	s := New(engine.New())
	args := make([][]byte, 0, 10002)
	args = append(args, []byte("PFADD"), []byte("large"))
	for i := 0; i < 10000; i++ {
		args = append(args, []byte("member:"+strconv.Itoa(i)))
	}
	response, err := s.Execute(args)
	if err != nil || string(response) != ":1\r\n" {
		t.Fatalf("large PFADD response=%q err=%v", response, err)
	}
	response, err = s.Execute(hllArgs("PFCOUNT", "large"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.TrimSuffix(strings.TrimPrefix(string(response), ":"), "\r\n")
	count, err := strconv.Atoi(text)
	if err != nil || count < 9500 || count > 10500 {
		t.Fatalf("large PFCOUNT = %q parsed=%d err=%v", response, count, err)
	}
}
