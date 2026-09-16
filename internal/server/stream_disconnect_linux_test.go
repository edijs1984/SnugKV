//go:build linux

package server

import (
	"io"
	"net"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func TestBlockedXReadClientDisconnectUnregistersWaiter(t *testing.T) {
	s, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	conn, err := net.DialTimeout("tcp", s.listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}

	wire := "*6\r\n$5\r\nXREAD\r\n$5\r\nBLOCK\r\n$1\r\n0\r\n$7\r\nSTREAMS\r\n$17\r\nstream:disconnect\r\n$1\r\n$\r\n"
	if _, err := io.WriteString(conn, wire); err != nil {
		conn.Close()
		t.Fatal(err)
	}

	waitForStreamRegistration(t, s.server, "stream:disconnect")
	waitForConnectionCount(t, s, 1)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	assertNoStreamRegistration(t, s.server, "stream:disconnect")
	waitForConnectionCount(t, s, 0)
}
