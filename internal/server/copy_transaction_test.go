package server

import (
	"bufio"
	"net"
	"testing"

	"snugkv/internal/engine"
)

func TestCopyInsideTransactionAndWatchInvalidation(t *testing.T) {
	tcp, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()

	a, err := net.Dial("tcp", tcp.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := net.Dial("tcp", tcp.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ra := bufio.NewReader(a)
	rb := bufio.NewReader(b)

	_ = txCommand(t, b, rb, "SET", "copy:src", "value")
	_ = txCommand(t, b, rb, "SET", "copy:dst", "old")

	if got := txCommand(t, a, ra, "WATCH", "copy:dst"); got != "+OK\r\n" {
		t.Fatalf("WATCH = %q", got)
	}
	if got := txCommand(t, b, rb, "COPY", "copy:src", "copy:dst", "REPLACE"); got != ":1\r\n" {
		t.Fatalf("COPY = %q", got)
	}
	_ = txCommand(t, a, ra, "MULTI")
	_ = txCommand(t, a, ra, "PING")
	if got := txCommand(t, a, ra, "EXEC"); got != "*-1\r\n" {
		t.Fatalf("COPY did not invalidate WATCH: %q", got)
	}

	_ = txCommand(t, a, ra, "MULTI")
	if got := txCommand(t, a, ra, "COPY", "copy:src", "copy:tx"); got != "+QUEUED\r\n" {
		t.Fatalf("queued COPY = %q", got)
	}
	if got := txCommand(t, a, ra, "EXEC"); got != "*1\r\n:1\r\n" {
		t.Fatalf("COPY EXEC = %q", got)
	}
	if got := txCommand(t, a, ra, "GET", "copy:tx"); got != "$5\r\nvalue\r\n" {
		t.Fatalf("transactional COPY result = %q", got)
	}
}
