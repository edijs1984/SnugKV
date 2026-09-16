package server

import (
	"strings"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func TestXReadGroupPendingAndAck(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "XADD", "events", "1-0", "v", "one")
	execute(t, s, "XADD", "events", "2-0", "v", "two")
	execute(t, s, "XGROUP", "CREATE", "events", "workers", "0-0")

	got := execute(t, s, "XREADGROUP", "GROUP", "workers", "c1", "COUNT", "1", "STREAMS", "events", ">")
	if !strings.Contains(got, "$3\r\n1-0\r\n") || strings.Contains(got, "$3\r\n2-0\r\n") {
		t.Fatalf("XREADGROUP=%q", got)
	}

	got = execute(t, s, "XPENDING", "events", "workers")
	want := "*4\r\n:1\r\n$3\r\n1-0\r\n$3\r\n1-0\r\n*1\r\n*2\r\n$2\r\nc1\r\n$1\r\n1\r\n"
	if got != want {
		t.Fatalf("XPENDING summary=%q want=%q", got, want)
	}

	got = execute(t, s, "XPENDING", "events", "workers", "-", "+", "10", "c1")
	if !strings.Contains(got, "$3\r\n1-0\r\n") || !strings.Contains(got, "$2\r\nc1\r\n") || !strings.HasSuffix(got, ":1\r\n") {
		t.Fatalf("XPENDING detail=%q", got)
	}

	got = execute(t, s, "XREADGROUP", "GROUP", "workers", "c1", "STREAMS", "events", "0-0")
	if !strings.Contains(got, "$3\r\n1-0\r\n") {
		t.Fatalf("history=%q", got)
	}
	got = execute(t, s, "XPENDING", "events", "workers", "-", "+", "10", "c1")
	if !strings.HasSuffix(got, ":2\r\n") {
		t.Fatalf("history did not increment deliveries: %q", got)
	}

	if got := execute(t, s, "XACK", "events", "workers", "1-0"); got != ":1\r\n" {
		t.Fatalf("XACK=%q", got)
	}
	if got := execute(t, s, "XACK", "events", "workers", "1-0"); got != ":0\r\n" {
		t.Fatalf("repeat XACK=%q", got)
	}
}

func TestXReadGroupNOACKAndDeletedPendingPayload(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "XADD", "noack", "1-0", "v", "one")
	execute(t, s, "XGROUP", "CREATE", "noack", "workers", "0-0")
	execute(t, s, "XREADGROUP", "GROUP", "workers", "c1", "NOACK", "STREAMS", "noack", ">")
	if got := execute(t, s, "XPENDING", "noack", "workers"); !strings.HasPrefix(got, "*4\r\n:0\r\n") {
		t.Fatalf("NOACK pending=%q", got)
	}

	execute(t, s, "XADD", "events", "1-0", "v", "one")
	execute(t, s, "XGROUP", "CREATE", "events", "workers", "0-0")
	execute(t, s, "XREADGROUP", "GROUP", "workers", "c1", "STREAMS", "events", ">")
	execute(t, s, "XDEL", "events", "1-0")
	got := execute(t, s, "XREADGROUP", "GROUP", "workers", "c1", "STREAMS", "events", "0-0")
	if !strings.Contains(got, "$3\r\n1-0\r\n*-1\r\n") {
		t.Fatalf("deleted pending payload=%q", got)
	}
}

func TestXGroupDelConsumerLeavesOrphanPending(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "XADD", "events", "1-0", "v", "one")
	execute(t, s, "XGROUP", "CREATE", "events", "workers", "0-0")
	execute(t, s, "XREADGROUP", "GROUP", "workers", "c1", "STREAMS", "events", ">")
	if got := execute(t, s, "XGROUP", "DELCONSUMER", "events", "workers", "c1"); got != ":1\r\n" {
		t.Fatalf("DELCONSUMER=%q", got)
	}
	got := execute(t, s, "XPENDING", "events", "workers")
	if !strings.HasPrefix(got, "*4\r\n:1\r\n") || !strings.HasSuffix(got, "*0\r\n") {
		t.Fatalf("orphan XPENDING=%q", got)
	}
	if got := execute(t, s, "XACK", "events", "workers", "1-0"); got != ":1\r\n" {
		t.Fatalf("ack orphan=%q", got)
	}
}

func TestBlockingXReadGroupWakesOnXAdd(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "XGROUP", "CREATE", "events", "workers", "0-0", "MKSTREAM")

	type result struct {
		response []byte
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := s.ExecuteWithCancel([][]byte{
			[]byte("XREADGROUP"), []byte("GROUP"), []byte("workers"), []byte("c1"),
			[]byte("BLOCK"), []byte("1000"), []byte("STREAMS"), []byte("events"), []byte(">"),
		}, nil)
		done <- result{response: response, err: err}
	}()

	waitForStreamRegistration(t, s, "events")
	execute(t, s, "XADD", "events", "1-0", "v", "new")

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if !strings.Contains(string(got.response), "$3\r\n1-0\r\n") {
			t.Fatalf("blocking XREADGROUP=%q", got.response)
		}
	case <-time.After(time.Second):
		t.Fatal("blocking XREADGROUP did not wake")
	}
	assertNoStreamRegistration(t, s, "events")
	pending := execute(t, s, "XPENDING", "events", "workers")
	if !strings.HasPrefix(pending, "*4\r\n:1\r\n") {
		t.Fatalf("blocking delivery was not added to PEL: %q", pending)
	}
}

func TestXReadGroupSyntaxAndWrongType(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "plain", "value")
	bad := [][][]byte{
		{[]byte("XREADGROUP"), []byte("GROUP"), []byte("g"), []byte("c"), []byte("STREAMS"), []byte("a")},
		{[]byte("XREADGROUP"), []byte("GROUP"), []byte("g"), []byte("c"), []byte("COUNT"), []byte("0"), []byte("STREAMS"), []byte("a"), []byte(">")},
		{[]byte("XREADGROUP"), []byte("GROUP"), []byte("g"), []byte("c"), []byte("BLOCK"), []byte("-1"), []byte("STREAMS"), []byte("a"), []byte(">")},
	}
	for _, args := range bad {
		if _, err := s.Execute(args); err == nil {
			t.Fatalf("accepted invalid XREADGROUP: %q", args)
		}
	}
	if _, err := s.Execute([][]byte{[]byte("XPENDING"), []byte("plain"), []byte("g")}); err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") {
		t.Fatalf("XPENDING wrongtype=%v", err)
	}
}
