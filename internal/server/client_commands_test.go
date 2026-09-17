package server

import (
	"bytes"
	"net"
	"testing"
)

func clientArgs(parts ...string) [][]byte {
	args := make([][]byte, len(parts))
	for i, part := range parts {
		args[i] = []byte(part)
	}
	return args
}

func newClientTestServer() *TCPServer {
	return &TCPServer{
		clients:     make(map[uint64]*clientSession),
		connections: make(map[net.Conn]struct{}),
	}
}

func newLocalClientSession(id uint64) *clientSession {
	return newClientSession(
		id,
		nil,
		"127.0.0.1:10000",
		"127.0.0.1:6380",
	)
}

func executeClientCommandForTest(
	s *TCPServer,
	session *clientSession,
	args ...string,
) (bool, []byte, error) {
	if session != nil && s.clientByID(session.id) == nil {
		s.registerClient(session)
	}

	return s.executeClientConnectionCommand(
		session,
		clientArgs(args...),
	)
}

func TestClientID(t *testing.T) {
	s := newClientTestServer()
	session := newLocalClientSession(42)

	handled, response, err := executeClientCommandForTest(
		s,
		session,
		"CLIENT",
		"ID",
	)
	if err != nil {
		t.Fatalf("CLIENT ID: %v", err)
	}
	if !handled {
		t.Fatal("CLIENT ID was not handled")
	}
	if string(response) != ":42\r\n" {
		t.Fatalf(
			"CLIENT ID response = %q, want %q",
			response,
			":42\\r\\n",
		)
	}
}

func TestClientSetNameGetName(t *testing.T) {
	s := newClientTestServer()
	session := newLocalClientSession(1)

	handled, response, err := executeClientCommandForTest(
		s,
		session,
		"CLIENT",
		"GETNAME",
	)
	if err != nil || !handled {
		t.Fatalf(
			"initial CLIENT GETNAME handled=%v err=%v",
			handled,
			err,
		)
	}

	if !bytes.Equal(response, nullBulk()) {
		t.Fatalf("initial GETNAME = %q", response)
	}

	_, response, err = executeClientCommandForTest(
		s,
		session,
		"CLIENT",
		"SETNAME",
		"snug-test",
	)
	if err != nil {
		t.Fatalf("CLIENT SETNAME: %v", err)
	}

	if string(response) != "+OK\r\n" {
		t.Fatalf("SETNAME response = %q", response)
	}

	_, response, err = executeClientCommandForTest(
		s,
		session,
		"CLIENT",
		"GETNAME",
	)
	if err != nil {
		t.Fatalf("CLIENT GETNAME: %v", err)
	}

	want := formatBulkString([]byte("snug-test"))

	if !bytes.Equal(response, want) {
		t.Fatalf(
			"GETNAME = %q, want %q",
			response,
			want,
		)
	}
}

func TestClientNameRejectsWhitespace(t *testing.T) {
	s := newClientTestServer()
	session := newLocalClientSession(1)

	_, _, err := executeClientCommandForTest(
		s,
		session,
		"CLIENT",
		"SETNAME",
		"bad name",
	)

	if err == nil {
		t.Fatal("expected CLIENT SETNAME whitespace error")
	}

	want := "ERR Client names cannot contain spaces, newlines or special characters."

	if err.Error() != want {
		t.Fatalf(
			"SETNAME error = %q, want %q",
			err.Error(),
			want,
		)
	}
}

func TestClientSetInfo(t *testing.T) {
	s := newClientTestServer()
	session := newLocalClientSession(1)

	_, response, err := executeClientCommandForTest(
		s,
		session,
		"CLIENT",
		"SETINFO",
		"LIB-NAME",
		"ioredis",
	)
	if err != nil {
		t.Fatalf("CLIENT SETINFO LIB-NAME: %v", err)
	}

	if string(response) != "+OK\r\n" {
		t.Fatalf("SETINFO response = %q", response)
	}

	snapshot := session.snapshot()

	if snapshot.libName != "ioredis" {
		t.Fatalf("libName = %q", snapshot.libName)
	}

	_, _, err = executeClientCommandForTest(
		s,
		session,
		"CLIENT",
		"SETINFO",
		"LIB-VER",
		"5.4.1",
	)
	if err != nil {
		t.Fatalf("CLIENT SETINFO LIB-VER: %v", err)
	}

	snapshot = session.snapshot()

	if snapshot.libVer != "5.4.1" {
		t.Fatalf("libVer = %q", snapshot.libVer)
	}
}

func TestClientUnknownSubcommand(t *testing.T) {
	s := newClientTestServer()
	session := newLocalClientSession(1)

	handled, _, err := executeClientCommandForTest(
		s,
		session,
		"CLIENT",
		"DOES-NOT-EXIST",
	)

	if !handled {
		t.Fatal("CLIENT unknown subcommand was not handled")
	}

	if err == nil {
		t.Fatal("expected unknown CLIENT subcommand error")
	}

	want := "ERR unknown subcommand 'DOES-NOT-EXIST'. Try CLIENT HELP."

	if err.Error() != want {
		t.Fatalf(
			"unknown subcommand error = %q, want %q",
			err.Error(),
			want,
		)
	}
}

func TestClientHelp(t *testing.T) {
	s := newClientTestServer()
	session := newLocalClientSession(1)

	_, response, err := executeClientCommandForTest(
		s,
		session,
		"CLIENT",
		"HELP",
	)
	if err != nil {
		t.Fatalf("CLIENT HELP: %v", err)
	}

	if !bytes.Contains(
		response,
		[]byte("+SETINFO <LIB-NAME|LIB-VER> <value>\r\n"),
	) {
		t.Fatalf(
			"CLIENT HELP missing simple-string SETINFO entry: %q",
			response,
		)
	}
}

func TestClientInfoBasicFields(t *testing.T) {
	s := newClientTestServer()
	session := newLocalClientSession(123)

	session.setName("tester")
	session.setLibName("ioredis")
	session.setLibVer("5.4.1")

	_, response, err := executeClientCommandForTest(
		s,
		session,
		"CLIENT",
		"INFO",
	)
	if err != nil {
		t.Fatalf("CLIENT INFO: %v", err)
	}

	for _, want := range [][]byte{
		[]byte("id=123"),
		[]byte("name=tester"),
		[]byte("lib-name=ioredis"),
		[]byte("lib-ver=5.4.1"),
	} {
		if !bytes.Contains(response, want) {
			t.Fatalf(
				"CLIENT INFO missing %q: %q",
				want,
				response,
			)
		}
	}
}
