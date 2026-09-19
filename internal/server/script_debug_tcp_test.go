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

func writeRESPCommand(t *testing.T, conn net.Conn, parts ...string) {
	t.Helper()

	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(parts))
	for _, part := range parts {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(part), part)
	}

	if _, err := io.WriteString(conn, b.String()); err != nil {
		t.Fatal(err)
	}
}

func readExactReply(t *testing.T, reader *bufio.Reader, want string) {
	t.Helper()

	got := make([]byte, len(want))
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("reply = %q, want %q", got, want)
	}
}

func newScriptDebugTCP(t *testing.T) (*TCPServer, net.Conn, *bufio.Reader) {
	t.Helper()

	s, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	conn, err := net.DialTimeout("tcp", s.listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}

	return s, conn, bufio.NewReader(conn)
}

func TestScriptDebugCommandSyntaxTCP(t *testing.T) {
	_, conn, reader := newScriptDebugTCP(t)

	for _, mode := range []string{"NO", "YES", "SYNC"} {
		writeRESPCommand(t, conn, "SCRIPT", "DEBUG", mode)
		readExactReply(t, reader, "+OK\r\n")
	}

	writeRESPCommand(t, conn, "SCRIPT", "DEBUG", "MAYBE")
	readExactReply(t, reader, "-ERR Use SCRIPT DEBUG YES/SYNC/NO\r\n")

	writeRESPCommand(t, conn, "SCRIPT", "DEBUG")
	readExactReply(
		t,
		reader,
		"-ERR wrong number of arguments for 'script|debug' command\r\n",
	)

	writeRESPCommand(t, conn, "SCRIPT", "DEBUG", "NO", "EXTRA")
	readExactReply(
		t,
		reader,
		"-ERR wrong number of arguments for 'script|debug' command\r\n",
	)
}

func TestScriptDebugAsyncContinueRollsBack(t *testing.T) {
	s, conn, reader := newScriptDebugTCP(t)

	source := "redis.call('SET','debug:key','written')\n" +
		"redis.call('INCR','debug:counter')\n" +
		"return redis.call('GET','debug:key')"

	writeRESPCommand(t, conn, "SCRIPT", "DEBUG", "YES")
	readExactReply(t, reader, "+OK\r\n")

	writeRESPCommand(t, conn, "EVAL", source, "0")
	readExactReply(
		t,
		reader,
		"*2\r\n"+
			"+* Stopped at 1, stop reason = step over\r\n"+
			"+-> 1   redis.call('SET','debug:key','written')\r\n",
	)

	writeRESPCommand(t, conn, "C")
	readExactReply(
		t,
		reader,
		"*1\r\n+<endsession>\r\n$7\r\nwritten\r\n",
	)

	if _, ok := s.server.store.Get("debug:key"); ok {
		t.Fatal("async debugger persisted debug:key")
	}
	if _, ok := s.server.store.Get("debug:counter"); ok {
		t.Fatal("async debugger persisted debug:counter")
	}
}

func TestScriptDebugSyncContinuePersists(t *testing.T) {
	s, conn, reader := newScriptDebugTCP(t)

	source := "redis.call('SET','debug:key','written')\n" +
		"redis.call('INCR','debug:counter')\n" +
		"return redis.call('GET','debug:key')"

	writeRESPCommand(t, conn, "SCRIPT", "DEBUG", "SYNC")
	readExactReply(t, reader, "+OK\r\n")

	writeRESPCommand(t, conn, "EVAL", source, "0")
	readExactReply(
		t,
		reader,
		"*2\r\n"+
			"+* Stopped at 1, stop reason = step over\r\n"+
			"+-> 1   redis.call('SET','debug:key','written')\r\n",
	)

	writeRESPCommand(t, conn, "CONTINUE")
	readExactReply(
		t,
		reader,
		"*1\r\n+<endsession>\r\n$7\r\nwritten\r\n",
	)

	if value, ok := s.server.store.Get("debug:key"); !ok || string(value) != "written" {
		t.Fatalf("debug:key = %q ok=%v", value, ok)
	}
	if value, ok := s.server.store.Get("debug:counter"); !ok || string(value) != "1" {
		t.Fatalf("debug:counter = %q ok=%v", value, ok)
	}
}
