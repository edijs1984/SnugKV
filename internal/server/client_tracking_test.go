package server

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func trackingRESP(parts ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(parts))
	for _, part := range parts {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(part), part)
	}
	return b.String()
}

func trackingWrite(t *testing.T, conn net.Conn, parts ...string) {
	t.Helper()
	if _, err := io.WriteString(conn, trackingRESP(parts...)); err != nil {
		t.Fatal(err)
	}
}

func trackingReadExact(t *testing.T, reader *bufio.Reader, want string) {
	t.Helper()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("reply = %q, want %q", got, want)
	}
}

func trackingPair(t *testing.T) (*TCPServer, net.Conn, *bufio.Reader, net.Conn, *bufio.Reader) {
	t.Helper()

	s, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	dial := func() (net.Conn, *bufio.Reader) {
		conn, err := net.DialTimeout("tcp", s.listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(conn)
		trackingWrite(t, conn, "HELLO", "3")

		// HELLO is a RESP3 map. Consume it structurally enough for these tests:
		// 7 key/value pairs, recursively skipping values.
		if err := discardRESPValue(reader); err != nil {
			t.Fatal(err)
		}
		return conn, reader
	}

	a, ar := dial()
	b, br := dial()
	return s, a, ar, b, br
}

func discardRESPValue(r *bufio.Reader) error {
	prefix, err := r.ReadByte()
	if err != nil {
		return err
	}

	readLine := func() (string, error) {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
	}

	switch prefix {
	case '+', '-', ':', '_', '#', ',':
		_, err := readLine()
		return err
	case '$', '=':
		line, err := readLine()
		if err != nil {
			return err
		}
		var n int
		if _, err := fmt.Sscanf(line, "%d", &n); err != nil {
			return err
		}
		if n < 0 {
			return nil
		}
		buf := make([]byte, n+2)
		_, err = io.ReadFull(r, buf)
		return err
	case '*', '>', '~':
		line, err := readLine()
		if err != nil {
			return err
		}
		var n int
		if _, err := fmt.Sscanf(line, "%d", &n); err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			if err := discardRESPValue(r); err != nil {
				return err
			}
		}
		return nil
	case '%':
		line, err := readLine()
		if err != nil {
			return err
		}
		var n int
		if _, err := fmt.Sscanf(line, "%d", &n); err != nil {
			return err
		}
		for i := 0; i < n*2; i++ {
			if err := discardRESPValue(r); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported RESP prefix %q", prefix)
	}
}

func TestClientTrackingDefaultRESP3Invalidation(t *testing.T) {
	_, a, ar, b, br := trackingPair(t)

	trackingWrite(t, a, "CLIENT", "TRACKING", "ON")
	trackingReadExact(t, ar, "+OK\r\n")

	trackingWrite(t, a, "GET", "track:key")
	trackingReadExact(t, ar, "_\r\n")

	trackingWrite(t, b, "SET", "track:key", "v1")
	trackingReadExact(t, br, "+OK\r\n")

	trackingReadExact(
		t,
		ar,
		">2\r\n$10\r\ninvalidate\r\n*1\r\n$9\r\ntrack:key\r\n",
	)
}

func TestClientTrackingNoLoopConsumesOwnTrackedKey(t *testing.T) {
	_, a, ar, b, br := trackingPair(t)

	trackingWrite(t, a, "CLIENT", "TRACKING", "ON", "NOLOOP")
	trackingReadExact(t, ar, "+OK\r\n")

	trackingWrite(t, a, "GET", "noloop:key")
	trackingReadExact(t, ar, "_\r\n")

	trackingWrite(t, a, "SET", "noloop:key", "mine")
	trackingReadExact(t, ar, "+OK\r\n")

	if err := a.SetReadDeadline(time.Now().Add(75 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := ar.Peek(1); err == nil {
		t.Fatal("NOLOOP unexpectedly delivered own invalidation")
	}
	if err := a.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}

	trackingWrite(t, b, "SET", "noloop:key", "other")
	trackingReadExact(t, br, "+OK\r\n")

	if err := a.SetReadDeadline(time.Now().Add(75 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := ar.Peek(1); err == nil {
		t.Fatal("tracked key was not consumed by own NOLOOP write")
	}
}

func TestClientTrackingOptInAndOptOut(t *testing.T) {
	t.Run("optin", func(t *testing.T) {
		_, a, ar, b, br := trackingPair(t)

		trackingWrite(t, a, "CLIENT", "TRACKING", "ON", "OPTIN")
		trackingReadExact(t, ar, "+OK\r\n")
		trackingWrite(t, a, "CLIENT", "CACHING", "YES")
		trackingReadExact(t, ar, "+OK\r\n")
		trackingWrite(t, a, "GET", "optin:key")
		trackingReadExact(t, ar, "_\r\n")
		trackingWrite(t, b, "SET", "optin:key", "x")
		trackingReadExact(t, br, "+OK\r\n")
		trackingReadExact(
			t,
			ar,
			">2\r\n$10\r\ninvalidate\r\n*1\r\n$9\r\noptin:key\r\n",
		)
	})

	t.Run("optout", func(t *testing.T) {
		_, a, ar, b, br := trackingPair(t)

		trackingWrite(t, a, "CLIENT", "TRACKING", "ON", "OPTOUT")
		trackingReadExact(t, ar, "+OK\r\n")
		trackingWrite(t, a, "CLIENT", "CACHING", "NO")
		trackingReadExact(t, ar, "+OK\r\n")
		trackingWrite(t, a, "GET", "optout:key")
		trackingReadExact(t, ar, "_\r\n")
		trackingWrite(t, b, "SET", "optout:key", "x")
		trackingReadExact(t, br, "+OK\r\n")

		if err := a.SetReadDeadline(time.Now().Add(75 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		if _, err := ar.Peek(1); err == nil {
			t.Fatal("OPTOUT CACHING NO key was tracked")
		}
	})
}

func TestClientTrackingBcastPrefix(t *testing.T) {
	_, a, ar, b, br := trackingPair(t)

	trackingWrite(t, a, "CLIENT", "TRACKING", "ON", "BCAST", "PREFIX", "foo:")
	trackingReadExact(t, ar, "+OK\r\n")

	trackingWrite(t, b, "SET", "foo:a", "1")
	trackingReadExact(t, br, "+OK\r\n")
	trackingReadExact(
		t,
		ar,
		">2\r\n$10\r\ninvalidate\r\n*1\r\n$5\r\nfoo:a\r\n",
	)

	trackingWrite(t, b, "SET", "other:a", "1")
	trackingReadExact(t, br, "+OK\r\n")

	if err := a.SetReadDeadline(time.Now().Add(75 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := ar.Peek(1); err == nil {
		t.Fatal("non-prefix BCAST write produced invalidation")
	}
}
