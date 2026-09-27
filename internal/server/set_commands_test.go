package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestSetCommands(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "SADD", "letters", "b", "a", "b", "c"); got != ":3\r\n" {
		t.Fatalf("SADD=%q", got)
	}
	if got := execute(t, s, "TYPE", "letters"); got != "+set\r\n" {
		t.Fatalf("TYPE=%q", got)
	}
	if got := execute(t, s, "SCARD", "letters"); got != ":3\r\n" {
		t.Fatalf("SCARD=%q", got)
	}
	if got := execute(t, s, "SISMEMBER", "letters", "a"); got != ":1\r\n" {
		t.Fatalf("SISMEMBER existing=%q", got)
	}
	if got := execute(t, s, "SISMEMBER", "letters", "z"); got != ":0\r\n" {
		t.Fatalf("SISMEMBER missing=%q", got)
	}
	if got := execute(t, s, "SMEMBERS", "letters"); got != "*3\r\n$1\r\na\r\n$1\r\nb\r\n$1\r\nc\r\n" {
		t.Fatalf("SMEMBERS=%q", got)
	}
	if got := execute(t, s, "SREM", "letters", "a", "missing"); got != ":1\r\n" {
		t.Fatalf("SREM=%q", got)
	}
	if got := execute(t, s, "SCARD", "missing"); got != ":0\r\n" {
		t.Fatalf("missing SCARD=%q", got)
	}
	if got := execute(t, s, "SMEMBERS", "missing"); got != "*0\r\n" {
		t.Fatalf("missing SMEMBERS=%q", got)
	}
}

func TestSetCommandsWrongType(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "plain", "value")

	commands := [][]string{
		{"SADD", "plain", "x"},
		{"SREM", "plain", "x"},
		{"SISMEMBER", "plain", "x"},
		{"SCARD", "plain"},
		{"SMEMBERS", "plain"},
	}
	for _, command := range commands {
		args := make([][]byte, len(command))
		for i := range command {
			args[i] = []byte(command[i])
		}
		_, err := s.Execute(args)
		if err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") {
			t.Fatalf("%v err=%v", command, err)
		}
	}
}

func TestSetRenamePreservesTypeAndTTL(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SADD", "source", "a", "b")
	execute(t, s, "PEXPIRE", "source", "60000")

	if got := execute(t, s, "RENAME", "source", "target"); got != "+OK\r\n" {
		t.Fatalf("RENAME=%q", got)
	}
	if got := execute(t, s, "TYPE", "target"); got != "+set\r\n" {
		t.Fatalf("TYPE target=%q", got)
	}
	if got := execute(t, s, "SISMEMBER", "target", "a"); got != ":1\r\n" {
		t.Fatalf("renamed SISMEMBER=%q", got)
	}
	pttl := execute(t, s, "PTTL", "target")
	if pttl == ":-1\r\n" || pttl == ":-2\r\n" {
		t.Fatalf("renamed SET lost TTL: %q", pttl)
	}
}


func TestSInterCardRedisSemantics(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SADD", "a", "1", "2", "3", "4")
	execute(t, s, "SADD", "b", "2", "3", "4", "5")
	execute(t, s, "SADD", "c", "3", "4", "6")

	if got := execute(t, s, "SINTERCARD", "3", "a", "b", "c"); got != ":2\r\n" {
		t.Fatalf("SINTERCARD=%q", got)
	}
	if got := execute(t, s, "SINTERCARD", "3", "a", "b", "c", "LIMIT", "1"); got != ":1\r\n" {
		t.Fatalf("SINTERCARD LIMIT 1=%q", got)
	}
	if got := execute(t, s, "SINTERCARD", "3", "a", "b", "c", "LIMIT", "0"); got != ":2\r\n" {
		t.Fatalf("SINTERCARD LIMIT 0=%q", got)
	}
	if got := execute(t, s, "SINTERCARD", "2", "a", "missing"); got != ":0\r\n" {
		t.Fatalf("SINTERCARD missing=%q", got)
	}
}

func TestSInterCardValidationAndWrongType(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "plain", "value")
	execute(t, s, "SADD", "set", "a")

	for _, command := range [][][]byte{
		{[]byte("SINTERCARD"), []byte("0")},
		{[]byte("SINTERCARD"), []byte("2"), []byte("set")},
		{[]byte("SINTERCARD"), []byte("1"), []byte("set"), []byte("LIMIT"), []byte("-1")},
		{[]byte("SINTERCARD"), []byte("1"), []byte("set"), []byte("BOGUS"), []byte("1")},
	} {
		if _, err := s.Execute(command); err == nil {
			t.Fatalf("expected SINTERCARD error for %q", command)
		}
	}

	if _, err := s.Execute([][]byte{
		[]byte("SINTERCARD"), []byte("2"), []byte("set"), []byte("plain"),
	}); err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") {
		t.Fatalf("expected WRONGTYPE, err=%v", err)
	}
}

func TestSInterCardCommandKeys(t *testing.T) {
	refs, err := commandKeys([][]byte{
		[]byte("SINTERCARD"), []byte("3"), []byte("a"), []byte("b"), []byte("c"), []byte("LIMIT"), []byte("1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 3 || string(refs[0].value) != "a" || string(refs[1].value) != "b" || string(refs[2].value) != "c" {
		t.Fatalf("SINTERCARD key refs=%v", refs)
	}
}
