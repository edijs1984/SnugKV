package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func mustStreamPolicyExec(t *testing.T, s *Server, args ...string) []byte {
	t.Helper()
	command := make([][]byte, len(args))
	for i := range args {
		command[i] = []byte(args[i])
	}
	response, err := s.Execute(command)
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return response
}

func prepareServerPolicyState(t *testing.T) *Server {
	t.Helper()
	s := New(engine.New())
	for _, id := range []string{"1-0", "2-0", "3-0"} {
		mustStreamPolicyExec(t, s, "XADD", "events", id, "v", id)
	}
	mustStreamPolicyExec(t, s, "XGROUP", "CREATE", "events", "g1", "0-0")
	mustStreamPolicyExec(t, s, "XGROUP", "CREATE", "events", "g2", "0-0")
	mustStreamPolicyExec(t, s, "XREADGROUP", "GROUP", "g1", "c1", "STREAMS", "events", ">")
	mustStreamPolicyExec(t, s, "XREADGROUP", "GROUP", "g2", "c2", "STREAMS", "events", ">")
	return s
}

func TestXDelexReferencePolicies(t *testing.T) {
	s := prepareServerPolicyState(t)

	response := mustStreamPolicyExec(t, s, "XDELEX", "events", "KEEPREF", "IDS", "1", "1-0")
	if string(response) != "*1\r\n:1\r\n" {
		t.Fatalf("KEEPREF = %q", response)
	}
	pending := mustStreamPolicyExec(t, s, "XPENDING", "events", "g1")
	if !strings.HasPrefix(string(pending), "*4\r\n:3\r\n") {
		t.Fatalf("KEEPREF XPENDING = %q", pending)
	}

	response = mustStreamPolicyExec(t, s, "XDELEX", "events", "IDS", "1", "1-0", "DELREF")
	if string(response) != "*1\r\n:-1\r\n" {
		t.Fatalf("dangling DELREF = %q", response)
	}
	pending = mustStreamPolicyExec(t, s, "XPENDING", "events", "g1")
	if !strings.HasPrefix(string(pending), "*4\r\n:2\r\n") {
		t.Fatalf("DELREF XPENDING = %q", pending)
	}

	response = mustStreamPolicyExec(t, s, "XDELEX", "events", "ACKED", "IDS", "1", "2-0")
	if string(response) != "*1\r\n:2\r\n" {
		t.Fatalf("pending ACKED = %q", response)
	}
	mustStreamPolicyExec(t, s, "XACK", "events", "g1", "2-0")
	mustStreamPolicyExec(t, s, "XACK", "events", "g2", "2-0")
	response = mustStreamPolicyExec(t, s, "XDELEX", "events", "ACKED", "IDS", "1", "2-0")
	if string(response) != "*1\r\n:1\r\n" {
		t.Fatalf("fully acked = %q", response)
	}
}

func TestXAckDelReferencePolicies(t *testing.T) {
	s := prepareServerPolicyState(t)

	response := mustStreamPolicyExec(t, s, "XACKDEL", "events", "g1", "ACKED", "IDS", "1", "1-0")
	if string(response) != "*1\r\n:2\r\n" {
		t.Fatalf("first ACKED = %q", response)
	}
	response = mustStreamPolicyExec(t, s, "XACKDEL", "events", "g2", "ACKED", "IDS", "1", "1-0")
	if string(response) != "*1\r\n:1\r\n" {
		t.Fatalf("second ACKED = %q", response)
	}

	response = mustStreamPolicyExec(t, s, "XACKDEL", "events", "g1", "KEEPREF", "IDS", "1", "2-0")
	if string(response) != "*1\r\n:1\r\n" {
		t.Fatalf("KEEPREF = %q", response)
	}
	response = mustStreamPolicyExec(t, s, "XACKDEL", "events", "g2", "IDS", "1", "2-0", "DELREF")
	if string(response) != "*1\r\n:1\r\n" {
		t.Fatalf("dangling DELREF = %q", response)
	}
	pending := mustStreamPolicyExec(t, s, "XPENDING", "events", "g2")
	if !strings.HasPrefix(string(pending), "*4\r\n:1\r\n") {
		t.Fatalf("DELREF did not clean g2 PEL: %q", pending)
	}
}

func TestXTrimAndXAddReferencePolicies(t *testing.T) {
	s := prepareServerPolicyState(t)
	response := mustStreamPolicyExec(t, s, "XTRIM", "events", "MAXLEN", "1", "DELREF")
	if string(response) != ":2\r\n" {
		t.Fatalf("XTRIM DELREF = %q", response)
	}
	pending := mustStreamPolicyExec(t, s, "XPENDING", "events", "g1")
	if !strings.HasPrefix(string(pending), "*4\r\n:1\r\n") {
		t.Fatalf("XTRIM DELREF XPENDING = %q", pending)
	}

	s = prepareServerPolicyState(t)
	response = mustStreamPolicyExec(t, s, "XADD", "events", "DELREF", "MAXLEN", "2", "4-0", "v", "four")
	if string(response) != "$3\r\n4-0\r\n" {
		t.Fatalf("XADD DELREF = %q", response)
	}
	pending = mustStreamPolicyExec(t, s, "XPENDING", "events", "g1")
	if !strings.HasPrefix(string(pending), "*4\r\n:1\r\n") {
		t.Fatalf("XADD DELREF XPENDING = %q", pending)
	}
}

func TestStreamReferencePolicySyntaxAndMissingKey(t *testing.T) {
	s := New(engine.New())
	response := mustStreamPolicyExec(t, s, "XDELEX", "missing", "ACKED", "IDS", "2", "1-0", "2-0")
	if string(response) != "*2\r\n:-1\r\n:-1\r\n" {
		t.Fatalf("missing XDELEX = %q", response)
	}
	response = mustStreamPolicyExec(t, s, "XACKDEL", "missing", "g", "KEEPREF", "IDS", "1", "1-0")
	if string(response) != "*1\r\n:-1\r\n" {
		t.Fatalf("missing XACKDEL = %q", response)
	}

	cases := [][][]byte{
		{[]byte("XDELEX"), []byte("events"), []byte("IDS"), []byte("2"), []byte("1-0")},
		{[]byte("XDELEX"), []byte("events"), []byte("KEEPREF"), []byte("DELREF"), []byte("IDS"), []byte("1"), []byte("1-0")},
		{[]byte("XACKDEL"), []byte("events"), []byte("g"), []byte("IDS"), []byte("0")},
		{[]byte("XTRIM"), []byte("events"), []byte("MAXLEN"), []byte("1"), []byte("KEEPREF"), []byte("DELREF")},
	}
	for _, args := range cases {
		if _, err := s.Execute(args); err == nil {
			t.Fatalf("expected syntax error for %#v", args)
		}
	}
}
