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
	command := func(line, want string) {
		t.Helper()
		if _, err := fmt.Fprintf(conn, "%s\r\n", line); err != nil {
			t.Fatal(err)
		}
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
	if _, err := fmt.Fprint(conn, "SLOWLOG GET 1\r\n"); err != nil {
		t.Fatal(err)
	}
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
