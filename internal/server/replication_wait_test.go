package server

import (
	"errors"
	"strings"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func TestReplicationWaitImmediateAcknowledged(t *testing.T) {
	s := New(engine.New())
	id, _, _ := s.replication.registerReplica(func([]byte) error { return nil })
	defer s.replication.unregisterReplica(id)
	s.replication.acknowledgeReplica(id, 120)

	got, err := s.executeReplicationWait(
		[][]byte{[]byte("WAIT"), []byte("1"), []byte("1000")},
		120,
		nil,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != ":1\r\n" {
		t.Fatalf("WAIT=%q want=:1", got)
	}
}

func TestReplicationWaitTimeoutReturnsCurrentCount(t *testing.T) {
	s := New(engine.New())
	id, _, _ := s.replication.registerReplica(func([]byte) error { return nil })
	defer s.replication.unregisterReplica(id)

	started := time.Now()
	got, err := s.executeReplicationWait(
		[][]byte{[]byte("WAIT"), []byte("1"), []byte("25")},
		50,
		nil,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != ":0\r\n" {
		t.Fatalf("WAIT=%q want=:0", got)
	}
	if elapsed := time.Since(started); elapsed < 15*time.Millisecond {
		t.Fatalf("WAIT returned too early: %v", elapsed)
	}
}

func TestReplicationWaitWakesOnACK(t *testing.T) {
	s := New(engine.New())
	id, _, _ := s.replication.registerReplica(func([]byte) error { return nil })
	defer s.replication.unregisterReplica(id)

	done := make(chan struct {
		value string
		err   error
	}, 1)
	go func() {
		value, err := s.executeReplicationWait(
			[][]byte{[]byte("WAIT"), []byte("1"), []byte("1000")},
			77,
			nil,
			true,
		)
		done <- struct {
			value string
			err   error
		}{string(value), err}
	}()

	time.Sleep(20 * time.Millisecond)
	s.replication.acknowledgeReplica(id, 77)

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.value != ":1\r\n" {
			t.Fatalf("WAIT=%q want=:1", result.value)
		}
	case <-time.After(time.Second):
		t.Fatal("WAIT did not wake after ACK")
	}
}

func TestReplicationWaitCancel(t *testing.T) {
	s := New(engine.New())
	cancel := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		_, err := s.executeReplicationWait(
			[][]byte{[]byte("WAIT"), []byte("1"), []byte("0")},
			10,
			cancel,
			true,
		)
		done <- err
	}()

	close(cancel)
	select {
	case err := <-done:
		if !errors.Is(err, errBlockingClientGone) {
			t.Fatalf("WAIT cancel err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WAIT did not cancel")
	}
}

func TestReplicationWaitArgumentValidation(t *testing.T) {
	s := New(engine.New())
	for _, args := range [][][]byte{
		{[]byte("WAIT")},
		{[]byte("WAIT"), []byte("-1"), []byte("10")},
		{[]byte("WAIT"), []byte("1"), []byte("-1")},
		{[]byte("WAIT"), []byte("x"), []byte("10")},
	} {
		if _, err := s.executeReplicationWait(args, 0, nil, false); err == nil {
			t.Fatalf("WAIT args %q unexpectedly accepted", args)
		}
	}
}

func TestReplicationWaitInsideExecDoesNotBlock(t *testing.T) {
	s := New(engine.New())
	session := newTransactionSession(s)
	session.multi = true
	session.waitTargetOffset = 100
	session.queue = [][][]byte{
		{[]byte("WAIT"), []byte("1"), []byte("10000")},
	}

	started := time.Now()
	got, err := session.exec()
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("WAIT inside EXEC blocked for %v", elapsed)
	}
	if string(got) != "*1\r\n:0\r\n" {
		t.Fatalf("EXEC WAIT=%q", got)
	}
}

func TestReplicationWaitCommandMetadata(t *testing.T) {
	info, ok := commandTable["WAIT"]
	if !ok {
		t.Fatal("WAIT missing from command table")
	}
	if info.min != 3 || info.max != 3 || info.write {
		t.Fatalf("WAIT command metadata=%+v", info)
	}

	s := New(engine.New())
	got, err := s.Execute([][]byte{[]byte("WAIT"), []byte("0"), []byte("0")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), ":") {
		t.Fatalf("WAIT response=%q", got)
	}
}
