package server

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"snugkv/internal/engine"
	"strconv"
	"strings"
	"testing"
	"time"
)

func pubSubWire(args ...string) []byte {
	out := []byte(fmt.Sprintf("*%d\r\n", len(args)))
	for _, arg := range args {
		out = append(out, fmt.Sprintf("$%d\r\n", len(arg))...)
		out = append(out, arg...)
		out = append(out, '\r', '\n')
	}
	return out
}

func readPubSubFrame(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	out := []byte(line)
	if len(line) == 0 {
		return nil, io.ErrUnexpectedEOF
	}
	switch line[0] {
	case '*':
		n, err := strconv.Atoi(strings.TrimSpace(line[1:]))
		if err != nil {
			return nil, err
		}
		for i := 0; i < n; i++ {
			item, err := readPubSubFrame(r)
			if err != nil {
				return nil, err
			}
			out = append(out, item...)
		}
	case '$':
		n, err := strconv.Atoi(strings.TrimSpace(line[1:]))
		if err != nil {
			return nil, err
		}
		if n >= 0 {
			payload := make([]byte, n+2)
			if _, err := io.ReadFull(r, payload); err != nil {
				return nil, err
			}
			out = append(out, payload...)
		}
	}
	return out, nil
}

func writePubSubCommand(t *testing.T, conn net.Conn, args ...string) {
	t.Helper()
	if err := conn.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(pubSubWire(args...)); err != nil {
		t.Fatal(err)
	}
}

func mustReadPubSubFrame(t *testing.T, conn net.Conn, r *bufio.Reader) string {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	frame, err := readPubSubFrame(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(frame)
}

func TestTCPPubSubDirectPatternModeAndReset(t *testing.T) {
	tcp, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()

	subscriber, err := net.Dial("tcp", tcp.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer subscriber.Close()
	publisher, err := net.Dial("tcp", tcp.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	subReader := bufio.NewReader(subscriber)
	pubReader := bufio.NewReader(publisher)

	writePubSubCommand(t, subscriber, "SUBSCRIBE", "news")
	if got, want := mustReadPubSubFrame(t, subscriber, subReader), "*3\r\n$9\r\nsubscribe\r\n$4\r\nnews\r\n:1\r\n"; got != want {
		t.Fatalf("SUBSCRIBE = %q, want %q", got, want)
	}

	writePubSubCommand(t, publisher, "PUBLISH", "news", "hello")
	if got := mustReadPubSubFrame(t, publisher, pubReader); got != ":1\r\n" {
		t.Fatalf("PUBLISH count = %q", got)
	}
	if got, want := mustReadPubSubFrame(t, subscriber, subReader), "*3\r\n$7\r\nmessage\r\n$4\r\nnews\r\n$5\r\nhello\r\n"; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}

	writePubSubCommand(t, subscriber, "PSUBSCRIBE", "n*")
	if got, want := mustReadPubSubFrame(t, subscriber, subReader), "*3\r\n$10\r\npsubscribe\r\n$2\r\nn*\r\n:2\r\n"; got != want {
		t.Fatalf("PSUBSCRIBE = %q, want %q", got, want)
	}
	writePubSubCommand(t, publisher, "PUBLISH", "news", "two")
	if got := mustReadPubSubFrame(t, publisher, pubReader); got != ":2\r\n" {
		t.Fatalf("direct+pattern PUBLISH count = %q", got)
	}
	if got := mustReadPubSubFrame(t, subscriber, subReader); !strings.Contains(got, "$7\r\nmessage\r\n") {
		t.Fatalf("direct message = %q", got)
	}
	if got := mustReadPubSubFrame(t, subscriber, subReader); !strings.Contains(got, "$8\r\npmessage\r\n") || !strings.Contains(got, "$2\r\nn*\r\n") {
		t.Fatalf("pattern message = %q", got)
	}

	writePubSubCommand(t, subscriber, "GET", "key")
	if got := mustReadPubSubFrame(t, subscriber, subReader); !strings.HasPrefix(got, "-ERR Can't execute 'get': only (P|S)SUBSCRIBE") {
		t.Fatalf("subscribed GET = %q", got)
	}
	writePubSubCommand(t, subscriber, "PING", "health")
	if got, want := mustReadPubSubFrame(t, subscriber, subReader), "*2\r\n$4\r\npong\r\n$6\r\nhealth\r\n"; got != want {
		t.Fatalf("subscribed PING = %q, want %q", got, want)
	}

	writePubSubCommand(t, publisher, "PUBSUB", "NUMSUB", "news")
	if got, want := mustReadPubSubFrame(t, publisher, pubReader), "*2\r\n$4\r\nnews\r\n:1\r\n"; got != want {
		t.Fatalf("PUBSUB NUMSUB = %q", got)
	}
	writePubSubCommand(t, publisher, "PUBSUB", "NUMPAT")
	if got := mustReadPubSubFrame(t, publisher, pubReader); got != ":1\r\n" {
		t.Fatalf("PUBSUB NUMPAT = %q", got)
	}

	writePubSubCommand(t, subscriber, "RESET")
	if got := mustReadPubSubFrame(t, subscriber, subReader); got != "+RESET\r\n" {
		t.Fatalf("RESET = %q", got)
	}
	writePubSubCommand(t, subscriber, "GET", "missing")
	if got := mustReadPubSubFrame(t, subscriber, subReader); got != "$-1\r\n" {
		t.Fatalf("GET after RESET = %q", got)
	}
	writePubSubCommand(t, publisher, "PUBLISH", "news", "after-reset")
	if got := mustReadPubSubFrame(t, publisher, pubReader); got != ":0\r\n" {
		t.Fatalf("PUBLISH after RESET = %q", got)
	}
}

func TestTCPPubSubDisconnectCleansSubscriptions(t *testing.T) {
	tcp, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()

	subscriber, err := net.Dial("tcp", tcp.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	subReader := bufio.NewReader(subscriber)
	publisher, err := net.Dial("tcp", tcp.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	pubReader := bufio.NewReader(publisher)

	writePubSubCommand(t, subscriber, "SUBSCRIBE", "cleanup")
	_ = mustReadPubSubFrame(t, subscriber, subReader)
	if err := subscriber.Close(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		writePubSubCommand(t, publisher, "PUBSUB", "NUMSUB", "cleanup")
		got := mustReadPubSubFrame(t, publisher, pubReader)
		if got == "*2\r\n$7\r\ncleanup\r\n:0\r\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("subscription not cleaned after disconnect: %q", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
