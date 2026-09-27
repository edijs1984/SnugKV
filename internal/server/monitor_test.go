package server

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func monitorTestSend(t *testing.T, conn net.Conn, args ...string) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, arg := range args { fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(arg), arg) }
	if _, err := io.WriteString(conn, b.String()); err != nil { t.Fatal(err) }
}

func monitorTestReply(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	line, err := r.ReadString('\n')
	if err != nil { t.Fatal(err) }
	if len(line) < 3 { t.Fatalf("invalid reply %q", line) }
	if line[0] == '*' || line[0] == '$' {
		n, err := strconv.Atoi(strings.TrimSuffix(line[1:], "\r\n"))
		if err != nil { t.Fatal(err) }
		if n < 0 { return line }
		if line[0] == '*' {
			for i := 0; i < n; i++ { monitorTestReply(t, r) }
		} else {
			data := make([]byte, n+2)
			if _, err := io.ReadFull(r, data); err != nil { t.Fatal(err) }
		}
	}
	return line
}

func TestMonitorTCPCommandsAndExec(t *testing.T) {
	s, err := Listen("127.0.0.1:0", engine.New())
	if err != nil { t.Fatal(err) }
	defer s.Close()
	dial := func() net.Conn {
		c, err := net.DialTimeout("tcp", s.listener.Addr().String(), time.Second)
		if err != nil { t.Fatal(err) }
		t.Cleanup(func() { c.Close() })
		c.SetDeadline(time.Now().Add(5*time.Second))
		return c
	}
	monitor, actor := dial(), dial()
	mr, ar := bufio.NewReader(monitor), bufio.NewReader(actor)
	monitorTestSend(t, monitor, "MONITOR")
	if got := monitorTestReply(t, mr); got != "+OK\r\n" { t.Fatalf("MONITOR=%q", got) }
	commands := [][]string{
		{"PING"}, {"SET", "monitor:a", "hello"}, {"GET", "monitor:a"},
		{"MULTI"}, {"SET", "monitor:b", "world"}, {"GET", "monitor:b"},
		{"EXEC"}, {"DEL", "monitor:a", "monitor:b"},
	}
	for _, args := range commands {
		monitorTestSend(t, actor, args...)
		if got := monitorTestReply(t, ar); strings.HasPrefix(got, "-") { t.Fatalf("command %v: %q", args, got) }
	}
	for _, args := range commands {
		line, err := mr.ReadString('\n')
		if err != nil { t.Fatal(err) }
		space := strings.IndexByte(line, ' ')
		if space < 2 { t.Fatalf("invalid event %q", line) }
		if _, err := strconv.ParseFloat(line[1:space], 64); err != nil { t.Fatal(err) }
		want := " [0 "+actor.LocalAddr().String()+"]"
		for _, arg := range args { want += " "+monitorQuote([]byte(arg)) }
		want += "\r\n"
		if line[space:] != want { t.Fatalf("event=%q want suffix=%q", line, want) }
	}
}

func TestMonitorBinaryEscaping(t *testing.T) {
	got := monitorQuote([]byte{'a', '"', '\\', '\n', '\r', '\t', 0, 255})
	want := "\"a\\\"\\\\\\n\\r\\t\\x00\\xff\""
	if got != want { t.Fatalf("quote=%q want=%q", got, want) }
}

func TestMonitorSensitiveCommandsExcluded(t *testing.T) {
	for _, name := range []string{"AUTH", "HELLO", "ACL", "CONFIG", "MIGRATE", "MONITOR"} {
		if monitorVisible(clientArgs(name, "secret")) { t.Fatalf("%s must not be emitted", name) }
	}
}
