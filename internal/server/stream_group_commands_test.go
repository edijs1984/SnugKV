package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestXGroupLifecycle(t *testing.T) {
	s := New(engine.New())

	if _, err := s.Execute([][]byte{[]byte("XGROUP"), []byte("CREATE"), []byte("events"), []byte("workers"), []byte("$")}); err == nil {
		t.Fatal("expected CREATE without MKSTREAM to fail")
	}
	response, err := s.Execute([][]byte{[]byte("XGROUP"), []byte("CREATE"), []byte("events"), []byte("workers"), []byte("$"), []byte("MKSTREAM")})
	if err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("CREATE = %q, %v", response, err)
	}
	if _, err := s.Execute([][]byte{[]byte("XGROUP"), []byte("CREATE"), []byte("events"), []byte("workers"), []byte("0-0")}); err == nil || !strings.Contains(err.Error(), "BUSYGROUP") {
		t.Fatalf("duplicate CREATE error = %v", err)
	}

	response, err = s.Execute([][]byte{[]byte("XGROUP"), []byte("CREATECONSUMER"), []byte("events"), []byte("workers"), []byte("c1")})
	if err != nil || string(response) != ":1\r\n" {
		t.Fatalf("CREATECONSUMER = %q, %v", response, err)
	}
	response, err = s.Execute([][]byte{[]byte("XGROUP"), []byte("CREATECONSUMER"), []byte("events"), []byte("workers"), []byte("c1")})
	if err != nil || string(response) != ":0\r\n" {
		t.Fatalf("repeat CREATECONSUMER = %q, %v", response, err)
	}

	response, err = s.Execute([][]byte{[]byte("XGROUP"), []byte("SETID"), []byte("events"), []byte("workers"), []byte("0-0"), []byte("ENTRIESREAD"), []byte("0")})
	if err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("SETID = %q, %v", response, err)
	}
	response, err = s.Execute([][]byte{[]byte("XGROUP"), []byte("DELCONSUMER"), []byte("events"), []byte("workers"), []byte("c1")})
	if err != nil || string(response) != ":0\r\n" {
		t.Fatalf("DELCONSUMER = %q, %v", response, err)
	}
	response, err = s.Execute([][]byte{[]byte("XGROUP"), []byte("DESTROY"), []byte("events"), []byte("workers")})
	if err != nil || string(response) != ":1\r\n" {
		t.Fatalf("DESTROY = %q, %v", response, err)
	}
}

func TestXGroupPreservesStreamTTL(t *testing.T) {
	s := New(engine.New())
	if _, err := s.Execute([][]byte{[]byte("XADD"), []byte("events"), []byte("1-0"), []byte("type"), []byte("login")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute([][]byte{[]byte("PEXPIRE"), []byte("events"), []byte("60000")}); err != nil {
		t.Fatal(err)
	}
	before, err := s.Execute([][]byte{[]byte("PTTL"), []byte("events")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute([][]byte{[]byte("XGROUP"), []byte("CREATE"), []byte("events"), []byte("workers"), []byte("0-0")}); err != nil {
		t.Fatal(err)
	}
	after, err := s.Execute([][]byte{[]byte("PTTL"), []byte("events")})
	if err != nil {
		t.Fatal(err)
	}
	if string(before) == ":-1\r\n" || string(after) == ":-1\r\n" || string(after) == ":-2\r\n" {
		t.Fatalf("TTL was not preserved: before=%q after=%q", before, after)
	}
}

func TestXGroupWrongTypeAndSyntax(t *testing.T) {
	s := New(engine.New())
	if _, err := s.Execute([][]byte{[]byte("SET"), []byte("plain"), []byte("value")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute([][]byte{[]byte("XGROUP"), []byte("CREATE"), []byte("plain"), []byte("workers"), []byte("0-0")}); err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE ") {
		t.Fatalf("wrongtype error = %v", err)
	}
	if _, err := s.Execute([][]byte{[]byte("XGROUP"), []byte("CREATE"), []byte("x"), []byte("g"), []byte("0-0"), []byte("BOGUS")}); err == nil {
		t.Fatal("expected syntax error")
	}
}
