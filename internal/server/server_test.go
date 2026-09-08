package server

import (
	"bytes"
	"testing"
	"time"

	"morphcache/internal/engine"
)

func TestExecutePingAndEcho(t *testing.T) {
	serv := New(engine.New())

	res, err := serv.Execute([][]byte{[]byte("PING")})
	if err != nil {
		t.Fatalf("PING failed: %v", err)
	}
	if got, want := string(res), "+PONG\r\n"; got != want {
		t.Fatalf("PING mismatch: got %q want %q", got, want)
	}

	res, err = serv.Execute([][]byte{[]byte("ECHO"), []byte("hello")})
	if err != nil {
		t.Fatalf("ECHO failed: %v", err)
	}
	if got, want := string(res), "$5\r\nhello\r\n"; got != want {
		t.Fatalf("ECHO mismatch: got %q want %q", got, want)
	}
}

func TestExecuteSetGetDel(t *testing.T) {
	serv := New(engine.New())

	_, err := serv.Execute([][]byte{[]byte("SET"), []byte("k"), []byte("v")})
	if err != nil {
		t.Fatalf("SET failed: %v", err)
	}

	res, err := serv.Execute([][]byte{[]byte("GET"), []byte("k")})
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	if !bytes.Equal(res, []byte("$1\r\nv\r\n")) {
		t.Fatalf("GET mismatch: got %q want %q", res, "$1\r\nv\r\n")
	}

	res, err = serv.Execute([][]byte{[]byte("DEL"), []byte("k")})
	if err != nil {
		t.Fatalf("DEL failed: %v", err)
	}
	if string(res) != ":1\r\n" {
		t.Fatalf("DEL mismatch: got %q want %q", res, ":1\r\n")
	}
}

func TestExecuteIncrAndTTL(t *testing.T) {
	serv := New(engine.New())

	_, err := serv.Execute([][]byte{[]byte("SET"), []byte("n"), []byte("7")})
	if err != nil {
		t.Fatalf("SET failed: %v", err)
	}

	res, err := serv.Execute([][]byte{[]byte("INCR"), []byte("n")})
	if err != nil {
		t.Fatalf("INCR failed: %v", err)
	}
	if string(res) != ":8\r\n" {
		t.Fatalf("INCR mismatch: got %q want %q", res, ":8\r\n")
	}

	_, err = serv.Execute([][]byte{[]byte("SET"), []byte("ttlkey"), []byte("v"), []byte("PX"), []byte("50")})
	if err != nil {
		t.Fatalf("SET TTL failed: %v", err)
	}

	time.Sleep(80 * time.Millisecond)
	res, err = serv.Execute([][]byte{[]byte("TTL"), []byte("ttlkey")})
	if err != nil {
		t.Fatalf("TTL failed: %v", err)
	}
	if string(res) != ":-2\r\n" {
		t.Fatalf("TTL mismatch: got %q want %q", res, ":-2\r\n")
	}
}

func TestLiveTTL(t *testing.T) {
	store := engine.New()
	store.Set("live", []byte("v"), 60000)
	serv := New(store)
	for _, cmd := range []string{"TTL", "PTTL"} {
		reply, err := serv.Execute([][]byte{[]byte(cmd), []byte("live")})
		if err != nil || len(reply) < 4 || reply[0] != ':' || reply[1] == '-' {
			t.Fatalf("%s: %q %v", cmd, reply, err)
		}
	}
}
