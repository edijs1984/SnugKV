package server

import (
	"io"
	"net"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func TestTCPServerCloseCancelsBlockedXRead(t *testing.T) {
	s, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}

	conn, err := net.DialTimeout("tcp", s.listener.Addr().String(), time.Second)
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	defer conn.Close()

	wire := "*6\r\n$5\r\nXREAD\r\n$5\r\nBLOCK\r\n$1\r\n0\r\n$7\r\nSTREAMS\r\n$15\r\nstream:shutdown\r\n$1\r\n$\r\n"
	if _, err := io.WriteString(conn, wire); err != nil {
		s.Close()
		t.Fatal(err)
	}
	waitForStreamRegistration(t, s.server, "stream:shutdown")

	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("TCP server close hung with blocked XREAD")
	}
}
