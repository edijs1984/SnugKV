package server

import (
	"bufio"
	"net"
	"snugkv/internal/engine"
	"testing"
	"time"
)

func TestTCPShardedPubSubLifecycle(t *testing.T) {
	tcp, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()

	subscriber, err := net.Dial("tcp", tcp.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer subscriber.Close()
	publisher, err := net.Dial("tcp", tcp.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	subReader := bufio.NewReader(subscriber)
	pubReader := bufio.NewReader(publisher)

	writePubSubCommand(t, subscriber, "SSUBSCRIBE", "orders")
	if got, want := mustReadPubSubFrame(t, subscriber, subReader), "*3\r\n$10\r\nssubscribe\r\n$6\r\norders\r\n:1\r\n"; got != want {
		t.Fatalf("SSUBSCRIBE = %q, want %q", got, want)
	}

	writePubSubCommand(t, publisher, "SPUBLISH", "orders", "hello")
	if got := mustReadPubSubFrame(t, publisher, pubReader); got != ":1\r\n" {
		t.Fatalf("SPUBLISH count = %q", got)
	}
	if got, want := mustReadPubSubFrame(t, subscriber, subReader), "*3\r\n$8\r\nsmessage\r\n$6\r\norders\r\n$5\r\nhello\r\n"; got != want {
		t.Fatalf("smessage = %q, want %q", got, want)
	}

	// A classic publish with the same channel name must not count shard subscribers.
	writePubSubCommand(t, publisher, "PUBLISH", "orders", "classic")
	if got := mustReadPubSubFrame(t, publisher, pubReader); got != ":0\r\n" {
		t.Fatalf("PUBLISH counted shard subscriber = %q", got)
	}

	writePubSubCommand(t, publisher, "PUBSUB", "SHARDNUMSUB", "orders")
	if got, want := mustReadPubSubFrame(t, publisher, pubReader), "*2\r\n$6\r\norders\r\n:1\r\n"; got != want {
		t.Fatalf("PUBSUB SHARDNUMSUB = %q, want %q", got, want)
	}
	writePubSubCommand(t, publisher, "PUBSUB", "SHARDCHANNELS", "ord*")
	if got, want := mustReadPubSubFrame(t, publisher, pubReader), "*1\r\n$6\r\norders\r\n"; got != want {
		t.Fatalf("PUBSUB SHARDCHANNELS = %q, want %q", got, want)
	}

	writePubSubCommand(t, subscriber, "SUNSUBSCRIBE", "orders")
	if got, want := mustReadPubSubFrame(t, subscriber, subReader), "*3\r\n$12\r\nsunsubscribe\r\n$6\r\norders\r\n:0\r\n"; got != want {
		t.Fatalf("SUNSUBSCRIBE = %q, want %q", got, want)
	}
	writePubSubCommand(t, subscriber, "GET", "missing")
	if got := mustReadPubSubFrame(t, subscriber, subReader); got != "$-1\r\n" {
		t.Fatalf("GET after SUNSUBSCRIBE = %q", got)
	}
}

func TestTCPShardedPubSubDisconnectCleanup(t *testing.T) {
	tcp, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()

	subscriber, err := net.Dial("tcp", tcp.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	subReader := bufio.NewReader(subscriber)
	publisher, err := net.Dial("tcp", tcp.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	pubReader := bufio.NewReader(publisher)

	writePubSubCommand(t, subscriber, "SSUBSCRIBE", "cleanup")
	_ = mustReadPubSubFrame(t, subscriber, subReader)
	if err := subscriber.Close(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		writePubSubCommand(t, publisher, "PUBSUB", "SHARDNUMSUB", "cleanup")
		got := mustReadPubSubFrame(t, publisher, pubReader)
		if got == "*2\r\n$7\r\ncleanup\r\n:0\r\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("shard subscription not cleaned after disconnect: %q", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
