//go:build linux

package server

import (
	"io"
	"net"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func waitForConnectionCount(t *testing.T, s *TCPServer, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		got := len(s.connections)
		s.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.mu.Lock()
	got := len(s.connections)
	s.mu.Unlock()
	t.Fatalf("connection count = %d, want %d", got, want)
}

func TestBlockedListClientDisconnectUnregistersWaiter(t *testing.T) {
	s, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	conn, err := net.DialTimeout("tcp", s.listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}

	// Include a pipelined PING after BLPOP. The disconnect detector must observe
	// the socket hangup without consuming that queued protocol data.
	wire := "*3\r\n$5\r\nBLPOP\r\n$15\r\nlist:disconnect\r\n$1\r\n0\r\n" +
		"*1\r\n$4\r\nPING\r\n"
	if _, err := io.WriteString(conn, wire); err != nil {
		conn.Close()
		t.Fatal(err)
	}

	waitForListRegistration(t, s.server, "list:disconnect")
	waitForConnectionCount(t, s, 1)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	assertNoListRegistration(t, s.server, "list:disconnect")
	waitForConnectionCount(t, s, 0)
}

func TestBlockedZSetClientDisconnectUnregistersWaiter(t *testing.T) {
	s, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	conn, err := net.DialTimeout("tcp", s.listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}

	wire := "*3\r\n$8\r\nBZPOPMIN\r\n$15\r\nzset:disconnect\r\n$1\r\n0\r\n"
	if _, err := io.WriteString(conn, wire); err != nil {
		conn.Close()
		t.Fatal(err)
	}

	waitForZSetRegistration(t, s.server, "zset:disconnect")
	waitForConnectionCount(t, s, 1)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	assertNoZSetRegistration(t, s.server, "zset:disconnect")
	waitForConnectionCount(t, s, 0)
}
