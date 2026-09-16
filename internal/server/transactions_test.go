package server

import (
	"bufio"
	"net"
	"snugkv/internal/engine"
	"snugkv/internal/persistence"
	"strings"
	"testing"
	"time"
)

func txCommand(t *testing.T, conn net.Conn, reader *bufio.Reader, args ...string) string {
	t.Helper()
	writePubSubCommand(t, conn, args...)
	return mustReadPubSubFrame(t, conn, reader)
}

func TestTCPTransactionBasicsRuntimeErrorsAndDiscard(t *testing.T) {
	tcp, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()

	conn, err := net.Dial("tcp", tcp.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := bufio.NewReader(conn)

	if got := txCommand(t, conn, r, "MULTI"); got != "+OK\r\n" {
		t.Fatalf("MULTI = %q", got)
	}
	if got := txCommand(t, conn, r, "SET", "a", "1"); got != "+QUEUED\r\n" {
		t.Fatalf("queued SET = %q", got)
	}
	if got := txCommand(t, conn, r, "GET", "a"); got != "+QUEUED\r\n" {
		t.Fatalf("queued GET = %q", got)
	}
	if got, want := txCommand(t, conn, r, "EXEC"), "*2\r\n+OK\r\n$1\r\n1\r\n"; got != want {
		t.Fatalf("EXEC = %q, want %q", got, want)
	}

	if got := txCommand(t, conn, r, "SET", "wrongtype", "value"); got != "+OK\r\n" {
		t.Fatalf("SET wrongtype seed = %q", got)
	}
	_ = txCommand(t, conn, r, "MULTI")
	if got := txCommand(t, conn, r, "LPUSH", "wrongtype", "x"); got != "+QUEUED\r\n" {
		t.Fatalf("queued LPUSH = %q", got)
	}
	if got := txCommand(t, conn, r, "SET", "after-error", "yes"); got != "+QUEUED\r\n" {
		t.Fatalf("queued SET after runtime error = %q", got)
	}
	got := txCommand(t, conn, r, "EXEC")
	if !strings.HasPrefix(got, "*2\r\n-WRONGTYPE ") || !strings.HasSuffix(got, "+OK\r\n") {
		t.Fatalf("runtime-error EXEC = %q", got)
	}
	if got := txCommand(t, conn, r, "GET", "after-error"); got != "$3\r\nyes\r\n" {
		t.Fatalf("later command did not execute = %q", got)
	}

	_ = txCommand(t, conn, r, "MULTI")
	_ = txCommand(t, conn, r, "SET", "discarded", "1")
	if got := txCommand(t, conn, r, "DISCARD"); got != "+OK\r\n" {
		t.Fatalf("DISCARD = %q", got)
	}
	if got := txCommand(t, conn, r, "GET", "discarded"); got != "$-1\r\n" {
		t.Fatalf("discarded value exists = %q", got)
	}
}

func TestTCPTransactionQueueErrorExecAbort(t *testing.T) {
	tcp, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	conn, err := net.Dial("tcp", tcp.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := bufio.NewReader(conn)

	_ = txCommand(t, conn, r, "MULTI")
	_ = txCommand(t, conn, r, "SET", "foo", "1")
	if got := txCommand(t, conn, r, "INCR", "foo", "extra"); !strings.HasPrefix(got, "-ERR wrong number of arguments") {
		t.Fatalf("queue error = %q", got)
	}
	_ = txCommand(t, conn, r, "SET", "bar", "2")
	if got := txCommand(t, conn, r, "EXEC"); got != "-EXECABORT Transaction discarded because of previous errors.\r\n" {
		t.Fatalf("EXECABORT = %q", got)
	}
	if got := txCommand(t, conn, r, "MGET", "foo", "bar"); got != "*2\r\n$-1\r\n$-1\r\n" {
		t.Fatalf("aborted transaction mutated data = %q", got)
	}
}

func TestTCPTransactionWatchAbortUnwatchAndRestoreDetection(t *testing.T) {
	tcp, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()

	a, err := net.Dial("tcp", tcp.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := net.Dial("tcp", tcp.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ra := bufio.NewReader(a)
	rb := bufio.NewReader(b)

	_ = txCommand(t, b, rb, "SET", "watched", "one")
	if got := txCommand(t, a, ra, "WATCH", "watched"); got != "+OK\r\n" {
		t.Fatalf("WATCH = %q", got)
	}
	_ = txCommand(t, b, rb, "SET", "watched", "two")
	_ = txCommand(t, a, ra, "MULTI")
	_ = txCommand(t, a, ra, "GET", "watched")
	if got := txCommand(t, a, ra, "EXEC"); got != "*-1\r\n" {
		t.Fatalf("watched EXEC = %q", got)
	}

	// A change followed by restoration must still invalidate WATCH.
	_ = txCommand(t, b, rb, "SET", "watched", "base")
	_ = txCommand(t, a, ra, "WATCH", "watched")
	_ = txCommand(t, b, rb, "SET", "watched", "temporary")
	_ = txCommand(t, b, rb, "SET", "watched", "base")
	_ = txCommand(t, a, ra, "MULTI")
	_ = txCommand(t, a, ra, "PING")
	if got := txCommand(t, a, ra, "EXEC"); got != "*-1\r\n" {
		t.Fatalf("restored watched key did not abort = %q", got)
	}

	_ = txCommand(t, a, ra, "WATCH", "watched")
	_ = txCommand(t, b, rb, "SET", "watched", "changed")
	if got := txCommand(t, a, ra, "UNWATCH"); got != "+OK\r\n" {
		t.Fatalf("UNWATCH = %q", got)
	}
	_ = txCommand(t, a, ra, "MULTI")
	_ = txCommand(t, a, ra, "PING")
	if got := txCommand(t, a, ra, "EXEC"); got != "*1\r\n+PONG\r\n" {
		t.Fatalf("EXEC after UNWATCH = %q", got)
	}
}

func TestTCPTransactionWatchExpiryAndBlockingCommandDoesNotBlock(t *testing.T) {
	tcp, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	conn, err := net.Dial("tcp", tcp.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := bufio.NewReader(conn)

	_ = txCommand(t, conn, r, "SET", "expiring", "1", "PX", "20")
	_ = txCommand(t, conn, r, "WATCH", "expiring")
	time.Sleep(30 * time.Millisecond)
	_ = txCommand(t, conn, r, "MULTI")
	_ = txCommand(t, conn, r, "PING")
	if got := txCommand(t, conn, r, "EXEC"); got != "*-1\r\n" {
		t.Fatalf("expired WATCH EXEC = %q", got)
	}

	_ = txCommand(t, conn, r, "MULTI")
	_ = txCommand(t, conn, r, "BLPOP", "empty-list", "0")
	if got := txCommand(t, conn, r, "EXEC"); got != "*1\r\n*-1\r\n" {
		t.Fatalf("BLPOP inside EXEC = %q", got)
	}
}

type transactionCaptureJournal struct {
	frames [][]persistence.Record
}

func (j *transactionCaptureJournal) Append(records []persistence.Record) error {
	copyRecords := make([]persistence.Record, len(records))
	copy(copyRecords, records)
	j.frames = append(j.frames, copyRecords)
	return nil
}

func TestTransactionAOFUsesSingleFrame(t *testing.T) {
	store := engine.New()
	s := New(store)
	journal := &transactionCaptureJournal{}
	s.SetJournal(journal)
	session := newTransactionSession(s)
	defer session.close()

	if handled, response, err := session.handleCommand(pubSubArgs("MULTI")); !handled || err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("MULTI handled=%t response=%q err=%v", handled, response, err)
	}
	_, _, _ = session.handleCommand(pubSubArgs("SET", "a", "1"))
	_, _, _ = session.handleCommand(pubSubArgs("SET", "b", "2"))
	_, response, err := session.handleCommand(pubSubArgs("EXEC"))
	if err != nil {
		t.Fatal(err)
	}
	if string(response) != "*2\r\n+OK\r\n+OK\r\n" {
		t.Fatalf("EXEC = %q", response)
	}
	if len(journal.frames) != 1 {
		t.Fatalf("journal frames = %d, want 1", len(journal.frames))
	}
	if len(journal.frames[0]) != 2 {
		t.Fatalf("transaction frame records = %d, want 2", len(journal.frames[0]))
	}
}
