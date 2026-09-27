package server

import (
	"bufio"
	"fmt"
	"strings"
	"testing"
)

func TestSlowlogTCPClientIdentity(t *testing.T) {
	conn := connectTestServer(t)
	reader := bufio.NewReader(conn)
	send := func(line string) {
		t.Helper()
		args := strings.Fields(line)
		var wire strings.Builder
		fmt.Fprintf(&wire, "*%d\r\n", len(args))
		for _, arg := range args {
			fmt.Fprintf(&wire, "$%d\r\n%s\r\n", len(arg), arg)
		}
		if _, err := fmt.Fprint(conn, wire.String()); err != nil {
			t.Fatal(err)
		}
	}
	command := func(line, want string) {
		t.Helper()
		send(line)
		got, err := reader.ReadString('\n')
		if err != nil || got != want+"\r\n" {
			t.Fatalf("%s: got %q err=%v, want %q", line, got, err, want)
		}
	}
	command("CLIENT SETNAME slowlog-audit", "+OK")
	command("CONFIG SET slowlog-log-slower-than 0", "+OK")
	command("SLOWLOG RESET", "+OK")
	command("PING", "+PONG")
	// Connection commands are handled separately. Renaming must not mutate
	// the identity already captured in the PING entry.
	command("CLIENT SETNAME renamed", "+OK")
	send("SLOWLOG GET 1")
	want := []string{
		"*1", "*6", "", "", "", "*1", "$4", "PING",
		fmt.Sprintf("$%d", len(conn.LocalAddr().String())),
		conn.LocalAddr().String(), "$13", "slowlog-audit",
	}
	for i, expected := range want {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		line = strings.TrimSuffix(line, "\r\n")
		if expected == "" {
			if !strings.HasPrefix(line, ":") {
				t.Fatalf("field %d: expected integer, got %q", i, line)
			}
		} else if line != expected {
			t.Fatalf("field %d: got %q want %q", i, line, expected)
		}
	}
}


func TestSlowlogTCPFastGetSetPaths(t *testing.T) {
	conn := connectTestServer(t)
	reader := bufio.NewReader(conn)

	send := func(args ...string) {
		t.Helper()
		var wire strings.Builder
		fmt.Fprintf(&wire, "*%d\r\n", len(args))
		for _, arg := range args {
			fmt.Fprintf(&wire, "$%d\r\n%s\r\n", len(arg), arg)
		}
		if _, err := fmt.Fprint(conn, wire.String()); err != nil {
			t.Fatal(err)
		}
	}
	readLine := func() string {
		t.Helper()
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		return line
	}

	send("CONFIG", "SET", "slowlog-log-slower-than", "0")
	if got := readLine(); got != "+OK\r\n" { t.Fatalf("CONFIG SET=%q", got) }
	send("CONFIG", "SET", "slowlog-max-len", "32")
	if got := readLine(); got != "+OK\r\n" { t.Fatalf("CONFIG SET maxlen=%q", got) }
	send("SLOWLOG", "RESET")
	if got := readLine(); got != "+OK\r\n" { t.Fatalf("SLOWLOG RESET=%q", got) }

	send("SET", "slowlog:fast", "hello")
	if got := readLine(); got != "+OK\r\n" { t.Fatalf("SET=%q", got) }
	send("GET", "slowlog:fast")
	if got := readLine(); got != "$5\r\n" { t.Fatalf("GET header=%q", got) }
	if got := readLine(); got != "hello\r\n" { t.Fatalf("GET body=%q", got) }

	send("SLOWLOG", "GET", "10")
	var dump strings.Builder
	for {
		line := readLine()
		dump.WriteString(line)
		if reader.Buffered() == 0 {
			break
		}
	}
	got := dump.String()
	if !strings.Contains(got, "$3\r\nSET\r\n") {
		t.Fatalf("SLOWLOG missing fast-path SET: %q", got)
	}
	if !strings.Contains(got, "$3\r\nGET\r\n") {
		t.Fatalf("SLOWLOG missing fast-path GET: %q", got)
	}
}
