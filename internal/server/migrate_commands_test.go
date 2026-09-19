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

func readTestRESPCommand(r *bufio.Reader) ([][]byte, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(line, "*") || !strings.HasSuffix(line, "\r\n") {
		return nil, fmt.Errorf("invalid array header %q", line)
	}
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(line, "*"), "\r\n"))
	if err != nil {
		return nil, err
	}
	out := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		header, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(header, "$") || !strings.HasSuffix(header, "\r\n") {
			return nil, fmt.Errorf("invalid bulk header %q", header)
		}
		size, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(header, "$"), "\r\n"))
		if err != nil || size < 0 {
			return nil, fmt.Errorf("invalid bulk size %q", header)
		}
		value := make([]byte, size+2)
		if _, err := io.ReadFull(r, value); err != nil {
			return nil, err
		}
		if string(value[size:]) != "\r\n" {
			return nil, fmt.Errorf("invalid bulk terminator")
		}
		out = append(out, append([]byte(nil), value[:size]...))
	}
	return out, nil
}

func startMigrateTarget(t *testing.T, replies []string, got chan<- [][][]byte) (host, port string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	addr := ln.Addr().(*net.TCPAddr)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		commands := make([][][]byte, 0, len(replies))
		for _, reply := range replies {
			command, err := readTestRESPCommand(r)
			if err != nil {
				return
			}
			commands = append(commands, command)
			_, _ = conn.Write([]byte(reply))
		}
		got <- commands
	}()

	return "127.0.0.1", strconv.Itoa(addr.Port)
}

func TestParseMigrateOptions(t *testing.T) {
	args := [][]byte{
		[]byte("MIGRATE"), []byte("127.0.0.1"), []byte("6379"), []byte(""),
		[]byte("0"), []byte("5000"),
		[]byte("COPY"), []byte("REPLACE"),
		[]byte("AUTH2"), []byte("user"), []byte("pass"),
		[]byte("KEYS"), []byte("a"), []byte("b"),
	}
	options, err := parseMigrateOptions(args)
	if err != nil {
		t.Fatal(err)
	}
	if !options.copy || !options.replace || string(options.username) != "user" || string(options.password) != "pass" {
		t.Fatalf("options = %#v", options)
	}
	if len(options.keys) != 2 || string(options.keys[0]) != "a" || string(options.keys[1]) != "b" {
		t.Fatalf("keys = %#v", options.keys)
	}
}

func TestMigrateKeysRequiresEmptyKeyArgument(t *testing.T) {
	args := [][]byte{
		[]byte("MIGRATE"), []byte("127.0.0.1"), []byte("6379"), []byte("x"),
		[]byte("0"), []byte("5000"), []byte("KEYS"), []byte("a"),
	}
	_, err := parseMigrateOptions(args)
	if err == nil || err.Error() != "ERR When using MIGRATE KEYS option, the key argument must be set to the empty string" {
		t.Fatalf("err = %v", err)
	}
}

func TestMigratePartialTargetErrorDeletesAcknowledgedKeys(t *testing.T) {
	got := make(chan [][][]byte, 1)
	host, port := startMigrateTarget(t,
		[]string{
			"+OK\r\n", // SELECT
			"+OK\r\n", // RESTORE a
			"-BUSYKEY Target key name already exists.\r\n", // RESTORE b
		},
		got,
	)

	s := New(engine.New())
	if response, err := s.Execute([][]byte{[]byte("SET"), []byte("a"), []byte("1")}); err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("SET a response=%q err=%v", response, err)
	}
	if response, err := s.Execute([][]byte{[]byte("SET"), []byte("b"), []byte("2")}); err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("SET b response=%q err=%v", response, err)
	}

	args := [][]byte{
		[]byte("MIGRATE"), []byte(host), []byte(port), []byte(""),
		[]byte("0"), []byte("5000"), []byte("KEYS"), []byte("a"), []byte("b"),
	}
	response, err := s.Execute(args)
	if err == nil || err.Error() != "ERR Target instance replied with error: BUSYKEY Target key name already exists." {
		t.Fatalf("MIGRATE response=%q err=%v", response, err)
	}
	if _, found := s.store.Get("a"); found {
		t.Fatal("acknowledged key a was not deleted")
	}
	if value, found := s.store.Get("b"); !found || string(value) != "2" {
		t.Fatalf("key b value=%q found=%v", value, found)
	}

	select {
	case commands := <-got:
		if len(commands) != 3 {
			t.Fatalf("commands=%d", len(commands))
		}
		if string(commands[0][0]) != "SELECT" || string(commands[0][1]) != "0" {
			t.Fatalf("SELECT = %#v", commands[0])
		}
		if string(commands[1][0]) != "RESTORE" || string(commands[1][1]) != "a" {
			t.Fatalf("RESTORE a = %#v", commands[1])
		}
		if string(commands[2][0]) != "RESTORE" || string(commands[2][1]) != "b" {
			t.Fatalf("RESTORE b = %#v", commands[2])
		}
	case <-time.After(time.Second):
		t.Fatal("target did not receive MIGRATE pipeline")
	}
}

func TestMigrateCopyPreservesSource(t *testing.T) {
	got := make(chan [][][]byte, 1)
	host, port := startMigrateTarget(t,
		[]string{"+OK\r\n", "+OK\r\n"},
		got,
	)

	s := New(engine.New())
	if _, err := s.Execute([][]byte{[]byte("SET"), []byte("copy"), []byte("v")}); err != nil {
		t.Fatal(err)
	}
	response, err := s.Execute([][]byte{
		[]byte("MIGRATE"), []byte(host), []byte(port), []byte("copy"),
		[]byte("0"), []byte("5000"), []byte("COPY"),
	})
	if err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("MIGRATE COPY response=%q err=%v", response, err)
	}
	if value, found := s.store.Get("copy"); !found || string(value) != "v" {
		t.Fatalf("source value=%q found=%v", value, found)
	}
	<-got
}

func TestMigrateMissingReturnsNoKeyWithoutConnecting(t *testing.T) {
	s := New(engine.New())
	response, err := s.Execute([][]byte{
		[]byte("MIGRATE"), []byte("127.0.0.1"), []byte("1"), []byte("missing"),
		[]byte("0"), []byte("-1"),
	})
	if err != nil || string(response) != "+NOKEY\r\n" {
		t.Fatalf("response=%q err=%v", response, err)
	}
}


func TestMigratePartialMovePersistsAcknowledgedDeletion(t *testing.T) {
	got := make(chan [][][]byte, 1)
	host, port := startMigrateTarget(t,
		[]string{
			"+OK\r\n",
			"+OK\r\n",
			"-BUSYKEY Target key name already exists.\r\n",
		},
		got,
	)

	store := engine.New()
	s := New(store)
	journal := &transactionCaptureJournal{}
	s.SetJournal(journal)

	if _, err := s.Execute([][]byte{[]byte("SET"), []byte("a"), []byte("1")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute([][]byte{[]byte("SET"), []byte("b"), []byte("2")}); err != nil {
		t.Fatal(err)
	}
	journal.frames = nil

	_, err := s.Execute([][]byte{
		[]byte("MIGRATE"), []byte(host), []byte(port), []byte(""),
		[]byte("0"), []byte("5000"), []byte("KEYS"), []byte("a"), []byte("b"),
	})
	if err == nil || err.Error() != "ERR Target instance replied with error: BUSYKEY Target key name already exists." {
		t.Fatalf("MIGRATE err=%v", err)
	}
	if len(journal.frames) != 1 {
		t.Fatalf("journal frames=%d, want 1", len(journal.frames))
	}
	if len(journal.frames[0]) != 2 {
		t.Fatalf("journal records=%d, want 2", len(journal.frames[0]))
	}

	deleted := map[string]bool{}
	for _, record := range journal.frames[0] {
		if record.Deleted {
			deleted[string(record.Key)] = true
		}
	}
	if !deleted["a"] {
		t.Fatalf("acknowledged key a deletion not journaled: %#v", journal.frames[0])
	}
	if deleted["b"] {
		t.Fatalf("failed key b deletion was journaled: %#v", journal.frames[0])
	}
	<-got
}

func TestMigrateMoveInvalidatesWatch(t *testing.T) {
	got := make(chan [][][]byte, 1)
	host, port := startMigrateTarget(t,
		[]string{"+OK\r\n", "+OK\r\n"},
		got,
	)

	tcp, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()

	watcher, err := net.Dial("tcp", tcp.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	mover, err := net.Dial("tcp", tcp.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer mover.Close()

	rw := bufio.NewReader(watcher)
	rm := bufio.NewReader(mover)

	if got := txCommand(t, mover, rm, "SET", "watched:migrate", "v"); got != "+OK\r\n" {
		t.Fatalf("SET = %q", got)
	}
	if got := txCommand(t, watcher, rw, "WATCH", "watched:migrate"); got != "+OK\r\n" {
		t.Fatalf("WATCH = %q", got)
	}
	if got := txCommand(t, mover, rm,
		"MIGRATE", host, port, "watched:migrate", "0", "5000",
	); got != "+OK\r\n" {
		t.Fatalf("MIGRATE = %q", got)
	}

	_ = txCommand(t, watcher, rw, "MULTI")
	_ = txCommand(t, watcher, rw, "PING")
	if got := txCommand(t, watcher, rw, "EXEC"); got != "*-1\r\n" {
		t.Fatalf("MIGRATE did not invalidate WATCH: %q", got)
	}
	<-got
}
