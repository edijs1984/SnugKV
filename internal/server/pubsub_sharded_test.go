package server

import (
	"bytes"
	"snugkv/internal/engine"
	"strings"
	"testing"
)

func TestShardedPubSubDeliveryCountsAndIntrospection(t *testing.T) {
	s := New(engine.New())
	var classicPush bytes.Buffer
	classic := newPubSubSession(s, func(frame []byte) error {
		_, err := classicPush.Write(frame)
		return err
	})
	defer classic.close()
	var shardPush bytes.Buffer
	shard := newPubSubSession(s, func(frame []byte) error {
		_, err := shardPush.Write(frame)
		return err
	})
	defer shard.close()

	if err := classic.subscribe(pubSubArgs("orders"), false); err != nil {
		t.Fatal(err)
	}
	classicPush.Reset()
	if err := shard.subscribeShard(pubSubArgs("orders", "orders:{eu}")); err != nil {
		t.Fatal(err)
	}
	wantConfirm := "*3\r\n$10\r\nssubscribe\r\n$6\r\norders\r\n:1\r\n" +
		"*3\r\n$10\r\nssubscribe\r\n$11\r\norders:{eu}\r\n:2\r\n"
	if got := shardPush.String(); got != wantConfirm {
		t.Fatalf("SSUBSCRIBE = %q, want %q", got, wantConfirm)
	}

	// Classic and sharded Pub/Sub are distinct namespaces even for the same name.
	shardPush.Reset()
	response, err := s.Execute(pubSubArgs("PUBLISH", "orders", "classic"))
	if err != nil || string(response) != ":1\r\n" {
		t.Fatalf("PUBLISH = %q, %v", response, err)
	}
	if shardPush.Len() != 0 {
		t.Fatalf("classic publish reached shard subscriber: %q", shardPush.String())
	}
	classicPush.Reset()
	response, err = s.Execute(pubSubArgs("SPUBLISH", "orders", "sharded"))
	if err != nil || string(response) != ":1\r\n" {
		t.Fatalf("SPUBLISH = %q, %v", response, err)
	}
	if classicPush.Len() != 0 {
		t.Fatalf("shard publish reached classic subscriber: %q", classicPush.String())
	}
	if got, want := shardPush.String(), "*3\r\n$8\r\nsmessage\r\n$6\r\norders\r\n$7\r\nsharded\r\n"; got != want {
		t.Fatalf("shard delivery = %q, want %q", got, want)
	}

	response, err = s.Execute(pubSubArgs("PUBSUB", "SHARDCHANNELS", "orders*"))
	if err != nil || string(response) != "*2\r\n$6\r\norders\r\n$11\r\norders:{eu}\r\n" {
		t.Fatalf("PUBSUB SHARDCHANNELS = %q, %v", response, err)
	}
	response, err = s.Execute(pubSubArgs("PUBSUB", "SHARDNUMSUB", "orders", "missing"))
	if err != nil || string(response) != "*4\r\n$6\r\norders\r\n:1\r\n$7\r\nmissing\r\n:0\r\n" {
		t.Fatalf("PUBSUB SHARDNUMSUB = %q, %v", response, err)
	}
}

func TestShardedPubSubCountsAreIndependentAndCleanup(t *testing.T) {
	s := New(engine.New())
	var pushed bytes.Buffer
	session := newPubSubSession(s, func(frame []byte) error {
		_, err := pushed.Write(frame)
		return err
	})
	defer session.close()

	if err := session.subscribe(pubSubArgs("classic"), false); err != nil {
		t.Fatal(err)
	}
	pushed.Reset()
	if err := session.subscribe(pubSubArgs("c*"), true); err != nil {
		t.Fatal(err)
	}
	pushed.Reset()
	if err := session.subscribeShard(pubSubArgs("shard-a", "shard-b")); err != nil {
		t.Fatal(err)
	}
	if got := pushed.String(); !strings.HasSuffix(got, ":2\r\n") || strings.Contains(got, ":3\r\n") {
		t.Fatalf("shard count included classic subscriptions: %q", got)
	}

	pushed.Reset()
	if err := session.unsubscribeShard(pubSubArgs("shard-a")); err != nil {
		t.Fatal(err)
	}
	if got, want := pushed.String(), "*3\r\n$12\r\nsunsubscribe\r\n$7\r\nshard-a\r\n:1\r\n"; got != want {
		t.Fatalf("SUNSUBSCRIBE one = %q, want %q", got, want)
	}
	if !session.active() {
		t.Fatal("session became inactive with classic subscriptions remaining")
	}

	pushed.Reset()
	if err := session.unsubscribeShard(nil); err != nil {
		t.Fatal(err)
	}
	if got, want := pushed.String(), "*3\r\n$12\r\nsunsubscribe\r\n$7\r\nshard-b\r\n:0\r\n"; got != want {
		t.Fatalf("SUNSUBSCRIBE all = %q, want %q", got, want)
	}
	if !session.active() {
		t.Fatal("session became inactive while classic subscriptions remain")
	}

	if err := session.reset(); err != nil {
		t.Fatal(err)
	}
	response, err := s.Execute(pubSubArgs("PUBSUB", "SHARDNUMSUB", "shard-b"))
	if err != nil || string(response) != "*2\r\n$7\r\nshard-b\r\n:0\r\n" {
		t.Fatalf("shard cleanup = %q, %v", response, err)
	}
}

func TestShardedPubSubSyntax(t *testing.T) {
	s := New(engine.New())
	for _, args := range [][][]byte{
		pubSubArgs("SPUBLISH", "only-channel"),
		pubSubArgs("PUBSUB", "SHARDCHANNELS", "a", "b"),
	} {
		if _, err := s.Execute(args); err == nil {
			t.Fatalf("expected error for %q", args)
		}
	}

	response, err := s.Execute(pubSubArgs("PUBSUB", "SHARDNUMSUB"))
	if err != nil || string(response) != "*0\r\n" {
		t.Fatalf("empty SHARDNUMSUB = %q, %v", response, err)
	}
}
