package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestSMoveCommandSemantics(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SADD", "source", "a", "b")
	execute(t, s, "SADD", "dest", "b", "c")

	if got := execute(t, s, "SMOVE", "source", "dest", "a"); got != ":1\r\n" {
		t.Fatalf("SMOVE a=%q", got)
	}
	if got := execute(t, s, "SMEMBERS", "source"); got != "*1\r\n$1\r\nb\r\n" {
		t.Fatalf("source=%q", got)
	}
	if got := execute(t, s, "SMEMBERS", "dest"); got != "*3\r\n$1\r\na\r\n$1\r\nb\r\n$1\r\nc\r\n" {
		t.Fatalf("dest=%q", got)
	}

	// Destination already has b: source still loses it and command returns 1.
	if got := execute(t, s, "SMOVE", "source", "dest", "b"); got != ":1\r\n" {
		t.Fatalf("SMOVE existing destination member=%q", got)
	}
	if got := execute(t, s, "TYPE", "source"); got != "+none\r\n" {
		t.Fatalf("empty source TYPE=%q", got)
	}

	execute(t, s, "SADD", "same", "x")
	if got := execute(t, s, "SMOVE", "same", "same", "x"); got != ":1\r\n" {
		t.Fatalf("same-key SMOVE=%q", got)
	}
	if got := execute(t, s, "SCARD", "same"); got != ":1\r\n" {
		t.Fatalf("same-key SCARD=%q", got)
	}
}

func TestSMoveWrongTypeOrdering(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "plain", "value")

	// Redis returns 0 before inspecting destination type when source is absent.
	if got := execute(t, s, "SMOVE", "missing", "plain", "x"); got != ":0\r\n" {
		t.Fatalf("missing source SMOVE=%q", got)
	}

	execute(t, s, "SADD", "source", "x")
	_, err := s.Execute([][]byte{[]byte("SMOVE"), []byte("source"), []byte("plain"), []byte("missing")})
	if err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") {
		t.Fatalf("wrong destination type err=%v", err)
	}
}

func TestSPopReplyShapesAndMutation(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "SPOP", "missing"); got != "$-1\r\n" {
		t.Fatalf("missing SPOP=%q", got)
	}
	if got := execute(t, s, "SPOP", "missing", "2"); got != "*0\r\n" {
		t.Fatalf("missing SPOP count=%q", got)
	}

	execute(t, s, "SADD", "set", "a", "b", "c", "d")
	if got := execute(t, s, "SPOP", "set", "0"); got != "*0\r\n" {
		t.Fatalf("SPOP zero=%q", got)
	}
	if got := execute(t, s, "SCARD", "set"); got != ":4\r\n" {
		t.Fatalf("SCARD after zero pop=%q", got)
	}

	got := execute(t, s, "SPOP", "set")
	if !strings.HasPrefix(got, "$1\r\n") {
		t.Fatalf("single SPOP shape=%q", got)
	}
	if card := execute(t, s, "SCARD", "set"); card != ":3\r\n" {
		t.Fatalf("SCARD after single pop=%q", card)
	}

	got = execute(t, s, "SPOP", "set", "2")
	if !strings.HasPrefix(got, "*2\r\n") {
		t.Fatalf("count SPOP shape=%q", got)
	}
	if card := execute(t, s, "SCARD", "set"); card != ":1\r\n" {
		t.Fatalf("SCARD after count pop=%q", card)
	}

	_, err := s.Execute([][]byte{[]byte("SPOP"), []byte("set"), []byte("-1")})
	if err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("negative SPOP err=%v", err)
	}
}

func TestSRandMemberReplyShapesAndNonMutation(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "SRANDMEMBER", "missing"); got != "$-1\r\n" {
		t.Fatalf("missing SRANDMEMBER=%q", got)
	}
	if got := execute(t, s, "SRANDMEMBER", "missing", "2"); got != "*0\r\n" {
		t.Fatalf("missing SRANDMEMBER count=%q", got)
	}

	execute(t, s, "SADD", "set", "a", "b", "c")
	if got := execute(t, s, "SRANDMEMBER", "set"); !strings.HasPrefix(got, "$1\r\n") {
		t.Fatalf("single SRANDMEMBER=%q", got)
	}
	if got := execute(t, s, "SRANDMEMBER", "set", "2"); !strings.HasPrefix(got, "*2\r\n") {
		t.Fatalf("positive SRANDMEMBER=%q", got)
	}
	if got := execute(t, s, "SRANDMEMBER", "set", "-5"); !strings.HasPrefix(got, "*5\r\n") {
		t.Fatalf("negative SRANDMEMBER=%q", got)
	}
	if got := execute(t, s, "SRANDMEMBER", "set", "0"); got != "*0\r\n" {
		t.Fatalf("zero SRANDMEMBER=%q", got)
	}
	if card := execute(t, s, "SCARD", "set"); card != ":3\r\n" {
		t.Fatalf("SRANDMEMBER mutated set: SCARD=%q", card)
	}
}

func TestSetMoveRandomWrongType(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "plain", "value")
	commands := [][]string{
		{"SPOP", "plain"},
		{"SRANDMEMBER", "plain"},
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
