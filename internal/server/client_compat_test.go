package server

import (
	"bufio"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func TestClientNoEvictNoTouchAndReplyState(t *testing.T) {
	s := newClientTestServer()
	session := newLocalClientSession(1)

	for _, tc := range []struct {
		args []string
	}{
		{[]string{"CLIENT", "NO-EVICT", "ON"}},
		{[]string{"CLIENT", "NO-TOUCH", "ON"}},
	} {
		_, response, err := executeClientCommandForTest(s, session, tc.args...)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if string(response) != "+OK\r\n" {
			t.Fatalf("%v response=%q", tc.args, response)
		}
	}

	snap := session.snapshot()
	if !snap.noEvict || !snap.noTouch {
		t.Fatalf("flags noEvict=%v noTouch=%v", snap.noEvict, snap.noTouch)
	}

	_, _, err := executeClientCommandForTest(s, session, "CLIENT", "REPLY", "OFF")
	if err != nil {
		t.Fatal(err)
	}
	if session.consumeReplyPermission() {
		t.Fatal("REPLY OFF still allows replies")
	}

	_, response, err := executeClientCommandForTest(s, session, "CLIENT", "REPLY", "ON")
	if err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("REPLY ON response=%q err=%v", response, err)
	}
}

func TestClientTrackingInfo(t *testing.T) {
	s := newClientTestServer()
	session := newLocalClientSession(1)
	target := newLocalClientSession(2)
	s.registerClient(session)
	s.registerClient(target)

	handled, response, err := s.executeClientTracking(
		session,
		clientArgs("CLIENT", "TRACKING", "ON", "BCAST", "NOLOOP", "PREFIX", "foo:", "REDIRECT", "2"),
	)
	if err != nil || !handled || string(response) != "+OK\r\n" {
		t.Fatalf("TRACKING handled=%v response=%q err=%v", handled, response, err)
	}

	handled, response, err = s.executeClientTracking(
		session,
		clientArgs("CLIENT", "TRACKINGINFO"),
	)
	if err != nil || !handled {
		t.Fatalf("TRACKINGINFO handled=%v err=%v", handled, err)
	}
	for _, want := range []string{"flags", "on", "bcast", "noloop", "redirect", "prefixes", "foo:"} {
		if !strings.Contains(string(response), want) {
			t.Fatalf("TRACKINGINFO missing %q: %q", want, response)
		}
	}
	if !strings.Contains(string(response), ":2\r\n") {
		t.Fatalf("TRACKINGINFO redirect=%q", response)
	}
}

func clientCompatWrite(t *testing.T, conn net.Conn, parts ...string) {
	t.Helper()
	if _, err := io.WriteString(conn, trackingRESP(parts...)); err != nil {
		t.Fatal(err)
	}
}

func TestClientReplyNetworkSemantics(t *testing.T) {
	s, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	conn, err := net.DialTimeout("tcp", s.listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)

	clientCompatWrite(t, conn, "CLIENT", "REPLY", "SKIP")
	trackingReadExact(t, reader, "+OK\r\n")
	clientCompatWrite(t, conn, "PING")

	if err := conn.SetReadDeadline(time.Now().Add(80 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Peek(1); err == nil {
		t.Fatal("REPLY SKIP did not suppress next reply")
	}

	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	clientCompatWrite(t, conn, "PING")
	trackingReadExact(t, reader, "+PONG\r\n")

	clientCompatWrite(t, conn, "CLIENT", "REPLY", "OFF")
	if err := conn.SetReadDeadline(time.Now().Add(80 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Peek(1); err == nil {
		t.Fatal("REPLY OFF command unexpectedly replied")
	}

	clientCompatWrite(t, conn, "PING")
	if _, err := reader.Peek(1); err == nil {
		t.Fatal("REPLY OFF did not suppress PING")
	}

	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	clientCompatWrite(t, conn, "CLIENT", "REPLY", "ON")
	trackingReadExact(t, reader, "+OK\r\n")
	clientCompatWrite(t, conn, "PING")
	trackingReadExact(t, reader, "+PONG\r\n")
}

func TestClientPauseWriteAndUnpause(t *testing.T) {
	s, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	dial := func() (net.Conn, *bufio.Reader) {
		c, err := net.DialTimeout("tcp", s.listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c, bufio.NewReader(c)
	}

	control, cr := dial()
	worker, wr := dial()

	clientCompatWrite(t, control, "CLIENT", "PAUSE", "500", "WRITE")
	trackingReadExact(t, cr, "+OK\r\n")

	clientCompatWrite(t, worker, "PING")
	trackingReadExact(t, wr, "+PONG\r\n")

	clientCompatWrite(t, worker, "SET", "paused:key", "value")
	if err := worker.SetReadDeadline(time.Now().Add(80 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := wr.Peek(1); err == nil {
		t.Fatal("write was not paused")
	}

	clientCompatWrite(t, control, "CLIENT", "UNPAUSE")
	trackingReadExact(t, cr, "+OK\r\n")

	if err := worker.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	trackingReadExact(t, wr, "+OK\r\n")
}

func TestClientPauseAll(t *testing.T) {
	s, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	control, err := net.DialTimeout("tcp", s.listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	cr := bufio.NewReader(control)

	worker, err := net.DialTimeout("tcp", s.listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	wr := bufio.NewReader(worker)

	clientCompatWrite(t, control, "CLIENT", "PAUSE", "120", "ALL")
	trackingReadExact(t, cr, "+OK\r\n")

	start := time.Now()
	clientCompatWrite(t, worker, "PING")
	trackingReadExact(t, wr, "+PONG\r\n")
	if time.Since(start) < 80*time.Millisecond {
		t.Fatalf("PAUSE ALL returned too early: %v", time.Since(start))
	}
}
