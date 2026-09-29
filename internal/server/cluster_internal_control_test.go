package server

import (
	"bufio"
	"fmt"
	"net"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func TestClusterInternalControlSessionAuthenticationTCP(t *testing.T) {
	target, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()

	addr := target.listener.Addr().String()
	target.server.clusterControlAuth = "cluster-control-secret"
	if err := target.server.configureClusterSlots(true, addr, map[string]string{
		"0-16383": addr,
	}); err != nil {
		t.Fatal(err)
	}
	digest := clusterOwnershipDigest(target.server.clusterStateSnapshot())

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)

	privateCheck := func() string {
		writeClusterRESPCommand(
			t,
			conn,
			"CLUSTER", "MEMBERSHIP", "CHECK", addr, digest,
		)
		return readRESPLine(t, reader)
	}

	if got := privateCheck(); got != "-NOPERM internal cluster control authentication required\r\n" {
		t.Fatalf("public private-RPC reply=%q", got)
	}

	writeClusterRESPCommand(t, conn, "SNUG.INTERNAL", "AUTH", "wrong-secret")
	if got := readRESPLine(t, reader); got != "-WRONGPASS invalid internal cluster control credentials\r\n" {
		t.Fatalf("wrong internal auth=%q", got)
	}
	if got := privateCheck(); got != "-NOPERM internal cluster control authentication required\r\n" {
		t.Fatalf("wrong secret unlocked control session: %q", got)
	}

	writeClusterRESPCommand(t, conn, "SNUG.INTERNAL", "AUTH", "cluster-control-secret")
	if got := readRESPLine(t, reader); got != "+OK\r\n" {
		t.Fatalf("internal auth=%q", got)
	}
	if got := privateCheck(); got != "+OK\r\n" {
		t.Fatalf("authenticated private RPC=%q", got)
	}

	writeClusterRESPCommand(t, conn, "RESET")
	if got := readRESPLine(t, reader); got != "+RESET\r\n" {
		t.Fatalf("RESET=%q", got)
	}
	if got := privateCheck(); got != "-NOPERM internal cluster control authentication required\r\n" {
		t.Fatalf("RESET did not revoke internal control: %q", got)
	}
}

func TestClusterControlPeerCommandNegotiatesInternalSession(t *testing.T) {
	target, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()

	addr := target.listener.Addr().String()
	target.server.clusterControlAuth = "peer-control-secret"
	if err := target.server.configureClusterSlots(true, addr, map[string]string{
		"0-16383": addr,
	}); err != nil {
		t.Fatal(err)
	}
	state := target.server.clusterStateSnapshot()

	source := New(engine.New())
	source.clusterControlAuth = "peer-control-secret"

	if err := source.sendClusterControlCommand(
		addr,
		"CLUSTER", "MEMBERSHIP", "CHECK",
		addr,
		clusterOwnershipDigest(state),
	); err != nil {
		t.Fatalf("authenticated peer control command: %v", err)
	}

	source.clusterControlAuth = "wrong-peer-secret"
	err = source.sendClusterControlCommand(
		addr,
		"CLUSTER", "MEMBERSHIP", "CHECK",
		addr,
		clusterOwnershipDigest(state),
	)
	if err == nil || err.Error() != "WRONGPASS invalid internal cluster control credentials" {
		t.Fatalf("wrong peer control secret err=%v", err)
	}
}

func TestClusterPrivateControlRPCCompatibilityModeWithoutSecret(t *testing.T) {
	s := New(engine.New())
	addr := "127.0.0.1:7000"
	if err := s.configureClusterSlots(true, addr, map[string]string{
		"0-16383": addr,
	}); err != nil {
		t.Fatal(err)
	}
	state := s.clusterStateSnapshot()

	client := newClientSession(1, nil, "client", addr)
	s.executionClient = client
	defer func() { s.executionClient = nil }()

	got, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("MEMBERSHIP"), []byte("CHECK"),
		[]byte(addr), []byte(clusterOwnershipDigest(state)),
	})
	if err != nil || string(got) != "+OK\r\n" {
		t.Fatalf("compatibility mode reply=%q err=%v", got, err)
	}

	_ = fmt.Sprintf("%p", client)
}
