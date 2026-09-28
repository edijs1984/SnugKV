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

func writeRESPCommand(t *testing.T, conn net.Conn, args ...string) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, arg := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(arg), arg)
	}
	if _, err := io.WriteString(conn, b.String()); err != nil {
		t.Fatal(err)
	}
}

func readRESPLine(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return line
}

func TestClusterAskingIsConnectionScopedTCP(t *testing.T) {
	tcp, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()

	local := tcp.listener.Addr().String()
	source := "127.0.0.1:7999"
	key := "foo"
	slot := clusterKeySlot([]byte(key))

	if err := tcp.server.configureClusterSlots(true, local, map[string]string{
		"0-8191":     local,
		"8192-16383": source,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := tcp.server.execute([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"),
		[]byte(strconv.Itoa(slot)), []byte("IMPORTING"), []byte(clusterNodeID(source)),
	}); err != nil {
		t.Fatal(err)
	}

	first, err := net.DialTimeout("tcp", local, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	first.SetDeadline(time.Now().Add(3 * time.Second))
	r1 := bufio.NewReader(first)

	second, err := net.DialTimeout("tcp", local, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	second.SetDeadline(time.Now().Add(3 * time.Second))
	r2 := bufio.NewReader(second)

	writeRESPCommand(t, first, "ASKING")
	if got := readRESPLine(t, r1); got != "+OK\r\n" {
		t.Fatalf("ASKING=%q", got)
	}

	writeRESPCommand(t, second, "GET", key)
	wantMoved := fmt.Sprintf("-MOVED %d %s\r\n", slot, source)
	if got := readRESPLine(t, r2); got != wantMoved {
		t.Fatalf("second client GET=%q want=%q", got, wantMoved)
	}

	writeRESPCommand(t, first, "SET", key, "value")
	if got := readRESPLine(t, r1); got != "+OK\r\n" {
		t.Fatalf("ASKING SET=%q", got)
	}

	writeRESPCommand(t, first, "GET", key)
	if got := readRESPLine(t, r1); got != wantMoved {
		t.Fatalf("ASKING should be one-shot: GET=%q want=%q", got, wantMoved)
	}

	writeRESPCommand(t, first, "ASKING")
	if got := readRESPLine(t, r1); got != "+OK\r\n" {
		t.Fatalf("second ASKING=%q", got)
	}
	writeRESPCommand(t, first, "GET", key)
	if got := readRESPLine(t, r1); got != "$5\r\n" {
		t.Fatalf("ASKING GET header=%q", got)
	}
	value, err := r1.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if value != "value\r\n" {
		t.Fatalf("ASKING GET value=%q", value)
	}
}
