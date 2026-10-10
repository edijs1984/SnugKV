package server

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"snugkv/internal/config"
	"snugkv/internal/engine"
)

func TestPubSubDropsSubscriberAfterFailedAttempts(t *testing.T) {
	s := New(engine.New())
	hub := pubSubHubForServer(s)
	hub.configure(3, 10*time.Millisecond, 4)

	var attempts atomic.Int32
	dropped := make(chan struct{})
	session := newPubSubSession(s, func([]byte) error { return nil })
	session.setPush(func([]byte, int, time.Duration) error {
		attempts.Add(1)
		return errors.New("stuck")
	}, func() { close(dropped) })
	if err := session.subscribe(pubSubArgs("news"), false); err != nil {
		t.Fatal(err)
	}

	if got := hub.publish([]byte("news"), []byte("a")); got != 1 {
		t.Fatalf("receivers = %d", got)
	}
	select {
	case <-dropped:
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber was not dropped")
	}
	if hub.dropped.Load() != 1 {
		t.Fatalf("dropped = %d", hub.dropped.Load())
	}
	if got := hub.publish([]byte("news"), []byte("b")); got != 0 {
		t.Fatalf("dropped subscriber still receives: %d", got)
	}
}

func TestPubSubDropsSubscriberWhenQueueIsFull(t *testing.T) {
	s := New(engine.New())
	hub := pubSubHubForServer(s)
	hub.configure(5, time.Second, 2)

	release := make(chan struct{})
	defer close(release)
	dropped := make(chan struct{})
	session := newPubSubSession(s, func([]byte) error { return nil })
	session.setPush(func([]byte, int, time.Duration) error {
		<-release
		return nil
	}, func() { close(dropped) })
	if err := session.subscribe(pubSubArgs("news"), false); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	for i := 0; i < 10; i++ {
		hub.publish([]byte("news"), []byte("x"))
	}
	if time.Since(start) > time.Second {
		t.Fatalf("PUBLISH blocked on a stuck subscriber for %v", time.Since(start))
	}
	select {
	case <-dropped:
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber with a full queue was not dropped")
	}
}

func TestWriteResumableKeepsStreamIntactAcrossTimeouts(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	data := bytes.Repeat([]byte("0123456789"), 400)
	got := make(chan []byte, 1)
	go func() {
		var out []byte
		buf := make([]byte, 700)
		for len(out) < len(data) {
			time.Sleep(30 * time.Millisecond) // slower than the 10ms attempt deadline
			n, err := client.Read(buf)
			out = append(out, buf[:n]...)
			if err != nil {
				break
			}
		}
		got <- out
	}()

	if err := writeResumable(server, data, 1000, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if out := <-got; !bytes.Equal(out, data) {
		t.Fatalf("stream corrupted: got %d bytes, want %d", len(out), len(data))
	}
}

func TestWriteResumableGivesUpWithoutProgress(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	start := time.Now()
	err := writeResumable(server, []byte("stuck"), 5, 20*time.Millisecond)
	if err == nil {
		t.Fatal("expected an error")
	}
	if elapsed := time.Since(start); elapsed < 90*time.Millisecond || elapsed > time.Second {
		t.Fatalf("gave up after %v, want about 5 x 20ms", elapsed)
	}
}

func TestTCPPubSubStuckSubscriberDoesNotBlockOthers(t *testing.T) {
	cfg := config.Default()
	cfg.ListenAddr = "127.0.0.1:0"
	cfg.PubSubSendAttempts = 2
	cfg.PubSubSendTimeoutMS = 50
	cfg.PubSubQueueSize = 16
	tcp, err := ListenWithConfig(cfg, engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()

	addr := tcp.listener.Addr().String()
	dial := func() (net.Conn, *bufio.Reader) {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		return conn, bufio.NewReader(conn)
	}

	stuck, stuckReader := dial()
	defer stuck.Close()
	_ = stuck.(*net.TCPConn).SetReadBuffer(1024)
	writePubSubCommand(t, stuck, "SUBSCRIBE", "news")
	mustReadPubSubFrame(t, stuck, stuckReader) // then never read again

	healthy, healthyReader := dial()
	defer healthy.Close()
	writePubSubCommand(t, healthy, "SUBSCRIBE", "news")
	mustReadPubSubFrame(t, healthy, healthyReader)

	publisher, pubReader := dial()
	defer publisher.Close()

	payload := strings.Repeat("x", 256<<10)
	const messages = 40
	start := time.Now()
	received := make(chan struct{})
	go func() {
		defer close(received)
		for i := 0; i < messages; i++ {
			frame := mustReadPubSubFrame(t, healthy, healthyReader)
			if !strings.Contains(frame, payload) {
				t.Errorf("healthy subscriber got a damaged message %d", i)
				return
			}
		}
	}()
	for i := 0; i < messages; i++ {
		writePubSubCommand(t, publisher, "PUBLISH", "news", payload)
		mustReadPubSubFrame(t, publisher, pubReader)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("publishing took %v with a stuck subscriber", elapsed)
	}

	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("healthy subscriber did not receive every message")
	}

	deadline := time.Now().Add(3 * time.Second)
	for pubSubHubForServer(tcp.server).dropped.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("stuck subscriber was never dropped")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
