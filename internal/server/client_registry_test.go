package server

import (
	"net"
	"strings"
	"testing"
	"time"
)

func testClientSession(
	id uint64,
	name string,
) (*clientSession, net.Conn, net.Conn) {
	a, b := net.Pipe()

	session := newClientSession(
		id,
		a,
		"127.0.0.1:10000",
		"127.0.0.1:6380",
	)

	session.setName(name)

	return session, a, b
}

func TestClientRegistry(t *testing.T) {
	s := &TCPServer{
		clients:     make(map[uint64]*clientSession),
		connections: make(map[net.Conn]struct{}),
	}

	c1, a1, b1 := testClientSession(10, "one")
	defer a1.Close()
	defer b1.Close()

	c2, a2, b2 := testClientSession(11, "two")
	defer a2.Close()
	defer b2.Close()

	s.registerClient(c1)
	s.registerClient(c2)

	if got := s.clientByID(10); got != c1 {
		t.Fatalf("clientByID(10) = %p, want %p", got, c1)
	}

	snapshots := s.clientSnapshots()
	if len(snapshots) != 2 {
		t.Fatalf("len(clientSnapshots) = %d, want 2", len(snapshots))
	}

	if snapshots[0].id != 10 || snapshots[1].id != 11 {
		t.Fatalf(
			"snapshot ids = %d,%d, want 10,11",
			snapshots[0].id,
			snapshots[1].id,
		)
	}

	s.unregisterClient(10)

	if got := s.clientByID(10); got != nil {
		t.Fatalf("client 10 still registered: %p", got)
	}
}

func TestClientInfo(t *testing.T) {
	s := &TCPServer{
		clients:     make(map[uint64]*clientSession),
		connections: make(map[net.Conn]struct{}),
	}

	client, a, b := testClientSession(42, "snug-test")
	defer a.Close()
	defer b.Close()

	client.setLibName("ioredis")
	client.setLibVer("5.4.1")
	client.touch(clientArgs("GET", "key"))

	s.registerClient(client)

	handled, response, err := s.executeClientConnectionCommand(
		client,
		clientArgs("CLIENT", "INFO"),
	)
	if err != nil {
		t.Fatalf("CLIENT INFO: %v", err)
	}
	if !handled {
		t.Fatal("CLIENT INFO not handled")
	}

	text := string(response)

	for _, want := range []string{
		"id=42",
		"name=snug-test",
		"cmd=get",
		"lib-name=ioredis",
		"lib-ver=5.4.1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("CLIENT INFO missing %q: %q", want, text)
		}
	}
}

func TestClientList(t *testing.T) {
	s := &TCPServer{
		clients:     make(map[uint64]*clientSession),
		connections: make(map[net.Conn]struct{}),
	}

	c1, a1, b1 := testClientSession(1, "first")
	defer a1.Close()
	defer b1.Close()

	c2, a2, b2 := testClientSession(2, "second")
	defer a2.Close()
	defer b2.Close()

	s.registerClient(c1)
	s.registerClient(c2)

	_, response, err := s.executeClientConnectionCommand(
		c1,
		clientArgs("CLIENT", "LIST"),
	)
	if err != nil {
		t.Fatalf("CLIENT LIST: %v", err)
	}

	text := string(response)

	if !strings.Contains(text, "id=1") {
		t.Fatalf("CLIENT LIST missing id=1: %q", text)
	}
	if !strings.Contains(text, "id=2") {
		t.Fatalf("CLIENT LIST missing id=2: %q", text)
	}
	if !strings.Contains(text, "name=first") {
		t.Fatalf("CLIENT LIST missing first client: %q", text)
	}
	if !strings.Contains(text, "name=second") {
		t.Fatalf("CLIENT LIST missing second client: %q", text)
	}
}

func TestClientUnblockState(t *testing.T) {
	client, a, b := testClientSession(7, "")
	defer a.Close()
	defer b.Close()

	ch := client.beginBlocking()

	if !client.requestUnblock(clientUnblockTimeout) {
		t.Fatal("requestUnblock returned false")
	}

	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("unblock channel was not closed")
	}

	mode, unblocked := client.endBlocking()

	if !unblocked {
		t.Fatal("expected unblocked state")
	}
	if mode != clientUnblockTimeout {
		t.Fatalf("mode = %v, want TIMEOUT", mode)
	}
}

func TestClientUnblockCommand(t *testing.T) {
	s := &TCPServer{
		clients:     make(map[uint64]*clientSession),
		connections: make(map[net.Conn]struct{}),
	}

	current, a1, b1 := testClientSession(1, "")
	defer a1.Close()
	defer b1.Close()

	target, a2, b2 := testClientSession(2, "")
	defer a2.Close()
	defer b2.Close()

	s.registerClient(current)
	s.registerClient(target)

	ch := target.beginBlocking()

	_, response, err := s.executeClientConnectionCommand(
		current,
		clientArgs("CLIENT", "UNBLOCK", "2", "ERROR"),
	)
	if err != nil {
		t.Fatalf("CLIENT UNBLOCK: %v", err)
	}

	if string(response) != ":1\r\n" {
		t.Fatalf("CLIENT UNBLOCK response = %q", response)
	}

	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("target was not unblocked")
	}

	mode, unblocked := target.endBlocking()
	if !unblocked || mode != clientUnblockError {
		t.Fatalf(
			"endBlocking = mode %v unblocked %v",
			mode,
			unblocked,
		)
	}
}

func TestClientUnblockMissingClient(t *testing.T) {
	s := &TCPServer{
		clients:     make(map[uint64]*clientSession),
		connections: make(map[net.Conn]struct{}),
	}

	current, a, b := testClientSession(1, "")
	defer a.Close()
	defer b.Close()

	s.registerClient(current)

	_, response, err := s.executeClientConnectionCommand(
		current,
		clientArgs("CLIENT", "UNBLOCK", "999"),
	)
	if err != nil {
		t.Fatalf("CLIENT UNBLOCK missing: %v", err)
	}

	if string(response) != ":0\r\n" {
		t.Fatalf("CLIENT UNBLOCK missing response = %q", response)
	}
}

func TestClientKillSkipMe(t *testing.T) {
	s := &TCPServer{
		clients:     make(map[uint64]*clientSession),
		connections: make(map[net.Conn]struct{}),
	}

	current, a, b := testClientSession(5, "")
	defer a.Close()
	defer b.Close()

	s.registerClient(current)

	_, response, err := s.executeClientConnectionCommand(
		current,
		clientArgs("CLIENT", "KILL", "ID", "5"),
	)
	if err != nil {
		t.Fatalf("CLIENT KILL self: %v", err)
	}

	if string(response) != ":0\r\n" {
		t.Fatalf("CLIENT KILL self response = %q", response)
	}
}

func TestClientBlockingTimeoutResponses(t *testing.T) {
	tests := []struct {
		cmd  string
		want string
	}{
		{"BLPOP", "*-1\r\n"},
		{"BRPOP", "*-1\r\n"},
		{"BZPOPMIN", "*-1\r\n"},
		{"BZPOPMAX", "*-1\r\n"},
		{"XREAD", "*-1\r\n"},
		{"XREADGROUP", "*-1\r\n"},
		{"BLMOVE", "$-1\r\n"},
		{"BRPOPLPUSH", "$-1\r\n"},
	}

	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			got := string(
				clientBlockingTimeoutResponse(
					clientArgs(tt.cmd),
				),
			)

			if got != tt.want {
				t.Fatalf(
					"%s timeout response = %q, want %q",
					tt.cmd,
					got,
					tt.want,
				)
			}
		})
	}
}

func TestClientListIDFilter(t *testing.T) {
	s := &TCPServer{
		clients:     make(map[uint64]*clientSession),
		connections: make(map[net.Conn]struct{}),
	}

	c1, a1, b1 := testClientSession(101, "one")
	defer a1.Close()
	defer b1.Close()

	c2, a2, b2 := testClientSession(102, "two")
	defer a2.Close()
	defer b2.Close()

	s.registerClient(c1)
	s.registerClient(c2)

	_, response, err := s.executeClientConnectionCommand(
		c1,
		clientArgs("CLIENT", "LIST", "ID", "102"),
	)
	if err != nil {
		t.Fatalf("CLIENT LIST ID: %v", err)
	}

	text := string(response)

	if strings.Contains(text, "id=101") {
		t.Fatalf("CLIENT LIST ID unexpectedly included id=101: %q", text)
	}

	if !strings.Contains(text, "id=102") {
		t.Fatalf("CLIENT LIST ID missing id=102: %q", text)
	}
}

func TestClientListTypeNormal(t *testing.T) {
	s := &TCPServer{
		clients:     make(map[uint64]*clientSession),
		connections: make(map[net.Conn]struct{}),
	}

	c, a, b := testClientSession(201, "normal")
	defer a.Close()
	defer b.Close()

	s.registerClient(c)

	_, response, err := s.executeClientConnectionCommand(
		c,
		clientArgs("CLIENT", "LIST", "TYPE", "NORMAL"),
	)
	if err != nil {
		t.Fatalf("CLIENT LIST TYPE NORMAL: %v", err)
	}

	if !strings.Contains(string(response), "id=201") {
		t.Fatalf("CLIENT LIST TYPE NORMAL missing client: %q", response)
	}
}

func TestClientKillWrongArity(t *testing.T) {
	s := &TCPServer{
		clients:     make(map[uint64]*clientSession),
		connections: make(map[net.Conn]struct{}),
	}

	c, a, b := testClientSession(301, "")
	defer a.Close()
	defer b.Close()

	s.registerClient(c)

	_, _, err := s.executeClientConnectionCommand(
		c,
		clientArgs("CLIENT", "KILL"),
	)

	if err == nil {
		t.Fatal("expected CLIENT KILL arity error")
	}

	want := "ERR wrong number of arguments for 'client|kill' command"

	if err.Error() != want {
		t.Fatalf(
			"CLIENT KILL error = %q, want %q",
			err.Error(),
			want,
		)
	}
}
