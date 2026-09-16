package server

import (
	"bytes"
	"snugkv/internal/engine"
	"strings"
	"testing"
)

func pubSubArgs(values ...string) [][]byte {
	out := make([][]byte, len(values))
	for i, value := range values {
		out[i] = []byte(value)
	}
	return out
}

func TestPubSubDirectPatternDeliveryAndIntrospection(t *testing.T) {
	s := New(engine.New())
	var pushed bytes.Buffer
	session := newPubSubSession(s, func(frame []byte) error {
		_, err := pushed.Write(frame)
		return err
	})
	defer session.close()

	if err := session.subscribe(pubSubArgs("news"), false); err != nil {
		t.Fatal(err)
	}
	if got, want := pushed.String(), "*3\r\n$9\r\nsubscribe\r\n$4\r\nnews\r\n:1\r\n"; got != want {
		t.Fatalf("subscribe = %q, want %q", got, want)
	}
	pushed.Reset()
	if err := session.subscribe(pubSubArgs("n*"), true); err != nil {
		t.Fatal(err)
	}
	if got, want := pushed.String(), "*3\r\n$10\r\npsubscribe\r\n$2\r\nn*\r\n:2\r\n"; got != want {
		t.Fatalf("psubscribe = %q, want %q", got, want)
	}

	// Duplicate subscription confirms the current total but does not add
	// another delivery path.
	pushed.Reset()
	if err := session.subscribe(pubSubArgs("news"), false); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(pushed.String(), ":2\r\n") {
		t.Fatalf("duplicate subscribe = %q", pushed.String())
	}

	pushed.Reset()
	response, err := s.Execute(pubSubArgs("PUBLISH", "news", "hello"))
	if err != nil || string(response) != ":2\r\n" {
		t.Fatalf("PUBLISH = %q, %v", response, err)
	}
	want := "*3\r\n$7\r\nmessage\r\n$4\r\nnews\r\n$5\r\nhello\r\n" +
		"*4\r\n$8\r\npmessage\r\n$2\r\nn*\r\n$4\r\nnews\r\n$5\r\nhello\r\n"
	if got := pushed.String(); got != want {
		t.Fatalf("delivery = %q, want %q", got, want)
	}

	response, err = s.Execute(pubSubArgs("PUBSUB", "CHANNELS", "n*"))
	if err != nil || string(response) != "*1\r\n$4\r\nnews\r\n" {
		t.Fatalf("PUBSUB CHANNELS = %q, %v", response, err)
	}
	response, err = s.Execute(pubSubArgs("PUBSUB", "NUMSUB", "news", "other"))
	if err != nil || string(response) != "*4\r\n$4\r\nnews\r\n:1\r\n$5\r\nother\r\n:0\r\n" {
		t.Fatalf("PUBSUB NUMSUB = %q, %v", response, err)
	}
	response, err = s.Execute(pubSubArgs("PUBSUB", "NUMPAT"))
	if err != nil || string(response) != ":1\r\n" {
		t.Fatalf("PUBSUB NUMPAT = %q, %v", response, err)
	}
}

func TestPubSubUnsubscribeResetAndSubscribedCommands(t *testing.T) {
	s := New(engine.New())
	var pushed bytes.Buffer
	session := newPubSubSession(s, func(frame []byte) error {
		_, err := pushed.Write(frame)
		return err
	})
	defer session.close()

	if err := session.subscribe(pubSubArgs("n*"), true); err != nil {
		t.Fatal(err)
	}
	pushed.Reset()
	if err := session.unsubscribe(nil, false); err != nil {
		t.Fatal(err)
	}
	if got, want := pushed.String(), "*3\r\n$11\r\nunsubscribe\r\n$-1\r\n:1\r\n"; got != want {
		t.Fatalf("empty UNSUBSCRIBE = %q, want %q", got, want)
	}

	pushed.Reset()
	if err := session.subscribedPing([]byte("health")); err != nil {
		t.Fatal(err)
	}
	if got, want := pushed.String(), "*2\r\n$4\r\npong\r\n$6\r\nhealth\r\n"; got != want {
		t.Fatalf("subscribed PING = %q, want %q", got, want)
	}

	handled, _, err := session.handleCommand(pubSubArgs("GET", "key"))
	if !handled || err == nil || !strings.Contains(err.Error(), "only (P|S)SUBSCRIBE") {
		t.Fatalf("subscribed GET handled=%t err=%v", handled, err)
	}

	pushed.Reset()
	if err := session.reset(); err != nil {
		t.Fatal(err)
	}
	if got := pushed.String(); got != "+RESET\r\n" {
		t.Fatalf("RESET = %q", got)
	}
	if session.active() {
		t.Fatal("session still active after RESET")
	}
	response, err := s.Execute(pubSubArgs("PUBSUB", "NUMPAT"))
	if err != nil || string(response) != ":0\r\n" {
		t.Fatalf("NUMPAT after reset = %q, %v", response, err)
	}
}

func TestPubSubMassUnsubscribeAndCloseCleanup(t *testing.T) {
	s := New(engine.New())
	var pushed bytes.Buffer
	session := newPubSubSession(s, func(frame []byte) error {
		_, err := pushed.Write(frame)
		return err
	})
	if err := session.subscribe(pubSubArgs("a", "b"), false); err != nil {
		t.Fatal(err)
	}
	pushed.Reset()
	if err := session.unsubscribe(nil, false); err != nil {
		t.Fatal(err)
	}
	want := "*3\r\n$11\r\nunsubscribe\r\n$1\r\na\r\n:1\r\n" +
		"*3\r\n$11\r\nunsubscribe\r\n$1\r\nb\r\n:0\r\n"
	if got := pushed.String(); got != want {
		t.Fatalf("mass unsubscribe = %q, want %q", got, want)
	}

	if err := session.subscribe(pubSubArgs("cleanup"), false); err != nil {
		t.Fatal(err)
	}
	session.close()
	response, err := s.Execute(pubSubArgs("PUBSUB", "NUMSUB", "cleanup"))
	if err != nil || string(response) != "*2\r\n$7\r\ncleanup\r\n:0\r\n" {
		t.Fatalf("cleanup NUMSUB = %q, %v", response, err)
	}
}
