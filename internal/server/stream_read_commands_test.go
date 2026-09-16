package server

import (
	"errors"
	"strings"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func TestXReadResponseAndCount(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "XADD", "events", "1-0", "f", "a")
	execute(t, s, "XADD", "events", "2-0", "f", "b")

	got := execute(t, s, "XREAD", "COUNT", "1", "STREAMS", "events", "0-0")
	want := "*1\r\n*2\r\n$6\r\nevents\r\n*1\r\n*2\r\n$3\r\n1-0\r\n*2\r\n$1\r\nf\r\n$1\r\na\r\n"
	if got != want {
		t.Fatalf("XREAD=%q want=%q", got, want)
	}

	if got := execute(t, s, "XREAD", "STREAMS", "events", "2-0"); got != "*-1\r\n" {
		t.Fatalf("XREAD at top=%q", got)
	}
	if got := execute(t, s, "XREAD", "STREAMS", "events", "$"); got != "*-1\r\n" {
		t.Fatalf("XREAD $=%q", got)
	}
}

func TestXReadMultipleStreams(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "XADD", "a", "1-0", "v", "a1")
	execute(t, s, "XADD", "b", "2-0", "v", "b2")

	got := execute(t, s, "XREAD", "STREAMS", "a", "b", "0-0", "0-0")
	if !strings.Contains(got, "$1\r\na\r\n") || !strings.Contains(got, "$1\r\nb\r\n") || !strings.Contains(got, "$3\r\n1-0\r\n") || !strings.Contains(got, "$3\r\n2-0\r\n") {
		t.Fatalf("multi XREAD=%q", got)
	}
}

func TestXReadSyntaxAndWrongType(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "plain", "value")

	bad := [][][]byte{
		{[]byte("XREAD"), []byte("STREAMS"), []byte("a")},
		{[]byte("XREAD"), []byte("COUNT"), []byte("0"), []byte("STREAMS"), []byte("a"), []byte("0-0")},
		{[]byte("XREAD"), []byte("BLOCK"), []byte("-1"), []byte("STREAMS"), []byte("a"), []byte("0-0")},
		{[]byte("XREAD"), []byte("NOPE"), []byte("1"), []byte("STREAMS"), []byte("a"), []byte("0-0")},
	}
	for _, args := range bad {
		if _, err := s.Execute(args); err == nil {
			t.Fatalf("accepted invalid XREAD: %q", args)
		}
	}

	_, err := s.Execute([][]byte{[]byte("XREAD"), []byte("STREAMS"), []byte("plain"), []byte("0-0")})
	if err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") {
		t.Fatalf("wrong type err=%v", err)
	}
}

func waitForStreamRegistration(t *testing.T, s *Server, key string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		registry := streamRegistryForServer(s)
		registry.mu.Lock()
		count := len(registry.byKey[key])
		registry.mu.Unlock()
		if count > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("stream waiter for %q was not registered", key)
}

func assertNoStreamRegistration(t *testing.T, s *Server, key string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		registry := streamRegistryForServer(s)
		registry.mu.Lock()
		count := len(registry.byKey[key])
		registry.mu.Unlock()
		if count == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("stream waiter for %q remained registered", key)
}

func TestBlockingXReadWakesOnXAdd(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "XADD", "events", "1-0", "v", "old")

	type result struct {
		response []byte
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := s.ExecuteWithCancel([][]byte{
			[]byte("XREAD"), []byte("BLOCK"), []byte("1000"),
			[]byte("STREAMS"), []byte("events"), []byte("$"),
		}, nil)
		done <- result{response: response, err: err}
	}()

	waitForStreamRegistration(t, s, "events")
	execute(t, s, "XADD", "events", "2-0", "v", "new")

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		text := string(got.response)
		if !strings.Contains(text, "$3\r\n2-0\r\n") || strings.Contains(text, "$3\r\n1-0\r\n") {
			t.Fatalf("blocking XREAD=%q", text)
		}
	case <-time.After(time.Second):
		t.Fatal("blocking XREAD did not wake")
	}
	assertNoStreamRegistration(t, s, "events")
}

func TestBlockingXReadTimeout(t *testing.T) {
	s := New(engine.New())
	response, err := s.ExecuteWithCancel([][]byte{
		[]byte("XREAD"), []byte("BLOCK"), []byte("20"),
		[]byte("STREAMS"), []byte("events"), []byte("0-0"),
	}, nil)
	if err != nil || string(response) != "*-1\r\n" {
		t.Fatalf("timeout response=%q err=%v", response, err)
	}
}

func TestBlockingXReadClientCancel(t *testing.T) {
	s := New(engine.New())
	cancel := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := s.ExecuteWithCancel([][]byte{
			[]byte("XREAD"), []byte("BLOCK"), []byte("0"),
			[]byte("STREAMS"), []byte("events"), []byte("$"),
		}, cancel)
		done <- err
	}()
	waitForStreamRegistration(t, s, "events")
	close(cancel)
	select {
	case err := <-done:
		if !errors.Is(err, errBlockingClientGone) {
			t.Fatalf("cancel err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("XREAD did not return after client cancellation")
	}
	assertNoStreamRegistration(t, s, "events")
}

func TestBlockingXReadServerCancel(t *testing.T) {
	s := New(engine.New())
	done := make(chan error, 1)
	go func() {
		_, err := s.Execute([][]byte{
			[]byte("XREAD"), []byte("BLOCK"), []byte("0"),
			[]byte("STREAMS"), []byte("events"), []byte("$"),
		})
		done <- err
	}()
	waitForStreamRegistration(t, s, "events")
	s.CancelBlockingStreams()
	select {
	case err := <-done:
		if !errors.Is(err, errBlockingCanceled) {
			t.Fatalf("shutdown err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("XREAD did not return after server cancellation")
	}
	assertNoStreamRegistration(t, s, "events")
}
