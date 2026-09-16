package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestXClaimTransfersOwnershipAndSupportsJustID(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "XADD", "events", "1-0", "v", "one")
	execute(t, s, "XGROUP", "CREATE", "events", "workers", "0-0")
	execute(t, s, "XREADGROUP", "GROUP", "workers", "c1", "STREAMS", "events", ">")

	got := execute(t, s, "XCLAIM", "events", "workers", "c2", "0", "1-0")
	if !strings.Contains(got, "$3\r\n1-0\r\n") || !strings.Contains(got, "$3\r\none\r\n") {
		t.Fatalf("XCLAIM=%q", got)
	}
	pending := execute(t, s, "XPENDING", "events", "workers", "-", "+", "10", "c2")
	if !strings.Contains(pending, "$2\r\nc2\r\n") || !strings.HasSuffix(pending, ":2\r\n") {
		t.Fatalf("pending after claim=%q", pending)
	}

	got = execute(t, s, "XCLAIM", "events", "workers", "c3", "0", "1-0", "JUSTID")
	if got != "*1\r\n$3\r\n1-0\r\n" {
		t.Fatalf("XCLAIM JUSTID=%q", got)
	}
	pending = execute(t, s, "XPENDING", "events", "workers", "-", "+", "10", "c3")
	if !strings.HasSuffix(pending, ":2\r\n") {
		t.Fatalf("JUSTID changed delivery count: %q", pending)
	}
}

func TestXClaimForceRetryCountAndDeletedCleanup(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "XADD", "events", "1-0", "v", "one")
	execute(t, s, "XADD", "events", "2-0", "v", "two")
	execute(t, s, "XGROUP", "CREATE", "events", "workers", "0-0")
	execute(t, s, "XREADGROUP", "GROUP", "workers", "c1", "COUNT", "1", "STREAMS", "events", ">")

	got := execute(t, s, "XCLAIM", "events", "workers", "c2", "0", "2-0", "FORCE", "RETRYCOUNT", "0")
	if !strings.Contains(got, "$3\r\n2-0\r\n") {
		t.Fatalf("force claim=%q", got)
	}
	pending := execute(t, s, "XPENDING", "events", "workers", "-", "+", "10", "c2")
	if !strings.HasSuffix(pending, ":0\r\n") {
		t.Fatalf("retrycount zero=%q", pending)
	}

	execute(t, s, "XDEL", "events", "1-0")
	if got := execute(t, s, "XCLAIM", "events", "workers", "c3", "0", "1-0"); got != "*0\r\n" {
		t.Fatalf("deleted XCLAIM=%q", got)
	}
	summary := execute(t, s, "XPENDING", "events", "workers")
	if !strings.HasPrefix(summary, "*4\r\n:1\r\n") {
		t.Fatalf("pending cleanup=%q", summary)
	}
}

func TestXAutoClaimReturnsCursorClaimsAndDeletedIDs(t *testing.T) {
	s := New(engine.New())
	for _, id := range []string{"1-0", "2-0", "3-0"} {
		execute(t, s, "XADD", "events", id, "v", id)
	}
	execute(t, s, "XGROUP", "CREATE", "events", "workers", "0-0")
	execute(t, s, "XREADGROUP", "GROUP", "workers", "c1", "STREAMS", "events", ">")
	execute(t, s, "XDEL", "events", "2-0")

	got := execute(t, s, "XAUTOCLAIM", "events", "workers", "c2", "0", "0-0", "COUNT", "1")
	if !strings.Contains(got, "$3\r\n1-0\r\n") || !strings.Contains(got, "$3\r\n2-0\r\n") {
		t.Fatalf("first XAUTOCLAIM=%q", got)
	}
	got = execute(t, s, "XAUTOCLAIM", "events", "workers", "c2", "0", "2-0", "COUNT", "10", "JUSTID")
	if !strings.Contains(got, "$3\r\n3-0\r\n") || !strings.Contains(got, "$3\r\n2-0\r\n") || !strings.HasPrefix(got, "*3\r\n$3\r\n0-0\r\n") {
		t.Fatalf("second XAUTOCLAIM=%q", got)
	}
}

func TestStreamClaimSyntaxAndWrongType(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "plain", "value")
	if _, err := s.Execute([][]byte{[]byte("XCLAIM"), []byte("plain"), []byte("g"), []byte("c"), []byte("0"), []byte("1-0")}); err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") {
		t.Fatalf("XCLAIM wrongtype=%v", err)
	}
	if _, err := s.Execute([][]byte{[]byte("XAUTOCLAIM"), []byte("plain"), []byte("g"), []byte("c"), []byte("0"), []byte("0-0")}); err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") {
		t.Fatalf("XAUTOCLAIM wrongtype=%v", err)
	}
	bad := [][][]byte{
		{[]byte("XCLAIM"), []byte("events"), []byte("g"), []byte("c"), []byte("-1"), []byte("1-0")},
		{[]byte("XCLAIM"), []byte("events"), []byte("g"), []byte("c"), []byte("0"), []byte("1-0"), []byte("IDLE")},
		{[]byte("XAUTOCLAIM"), []byte("events"), []byte("g"), []byte("c"), []byte("0"), []byte("0-0"), []byte("COUNT"), []byte("0")},
	}
	for _, args := range bad {
		if _, err := s.Execute(args); err == nil {
			t.Fatalf("accepted invalid claim command: %q", args)
		}
	}
}
