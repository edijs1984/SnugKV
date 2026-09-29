package server

import (
	"bufio"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func dialClusterClient(t *testing.T, addr string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	return conn, bufio.NewReader(conn)
}

func TestClusterTCPMovedTracksFailoverOwnershipChange(t *testing.T) {
	oldTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil { t.Fatal(err) }
	defer oldTCP.Close()
	newTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil { t.Fatal(err) }
	defer newTCP.Close()

	oldAddr := oldTCP.listener.Addr().String()
	newAddr := newTCP.listener.Addr().String()
	key := "client:failover"
	slot := clusterKeySlot([]byte(key))

	ranges := map[string]string{
		fmt.Sprintf("%d", slot): oldAddr,
	}
	if err := oldTCP.server.configureClusterSlots(true, oldAddr, ranges); err != nil { t.Fatal(err) }
	if err := newTCP.server.configureClusterSlots(true, newAddr, ranges); err != nil { t.Fatal(err) }

	_, newReader := dialClusterClient(t, newAddr)
	newConn, newReader := dialClusterClient(t, newAddr)
	writeClusterRESPCommand(t, newConn, "GET", key)
	wantOld := fmt.Sprintf("-MOVED %d %s\r\n", slot, oldAddr)
	if got := readRESPLine(t, newReader); got != wantOld {
		t.Fatalf("before failover GET=%q want=%q", got, wantOld)
	}

	if err := oldTCP.server.replaceClusterOwner(oldAddr, newAddr); err != nil { t.Fatal(err) }
	if err := newTCP.server.replaceClusterOwner(oldAddr, newAddr); err != nil { t.Fatal(err) }

	oldConn, oldReader := dialClusterClient(t, oldAddr)
	writeClusterRESPCommand(t, oldConn, "GET", key)
	wantNew := fmt.Sprintf("-MOVED %d %s\r\n", slot, newAddr)
	if got := readRESPLine(t, oldReader); got != wantNew {
		t.Fatalf("old primary GET=%q want=%q", got, wantNew)
	}

	writeClusterRESPCommand(t, newConn, "SET", key, "after")
	if got := readRESPLine(t, newReader); got != "+OK\r\n" {
		t.Fatalf("new primary SET=%q", got)
	}
	writeClusterRESPCommand(t, newConn, "GET", key)
	if got := readRESPLine(t, newReader); got != "$5\r\n" {
		t.Fatalf("new primary GET header=%q", got)
	}
	if got := readRESPLine(t, newReader); got != "after\r\n" {
		t.Fatalf("new primary GET value=%q", got)
	}
}

func TestClusterTCPAskRedirectAndOneShotAskingEndToEnd(t *testing.T) {
	sourceTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil { t.Fatal(err) }
	defer sourceTCP.Close()
	targetTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil { t.Fatal(err) }
	defer targetTCP.Close()

	sourceAddr := sourceTCP.listener.Addr().String()
	targetAddr := targetTCP.listener.Addr().String()
	key := "ask:e2e"
	slot := clusterKeySlot([]byte(key))
	ranges := map[string]string{fmt.Sprintf("%d", slot): sourceAddr}
	if err := sourceTCP.server.configureClusterSlots(true, sourceAddr, ranges); err != nil { t.Fatal(err) }
	if err := targetTCP.server.configureClusterSlots(true, targetAddr, ranges); err != nil { t.Fatal(err) }

	sourceTCP.server.clusterMu.Lock()
	sourceTCP.server.clusterKnownNodes[targetAddr] = struct{}{}
	sourceTCP.server.clusterMu.Unlock()
	targetTCP.server.clusterMu.Lock()
	targetTCP.server.clusterKnownNodes[sourceAddr] = struct{}{}
	targetTCP.server.clusterMu.Unlock()

	if _, err := targetTCP.server.executeClusterSetSlot([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"), []byte(strconv.Itoa(slot)),
		[]byte("IMPORTING"), []byte(clusterNodeID(sourceAddr)),
	}); err != nil { t.Fatal(err) }
	if _, err := sourceTCP.server.executeClusterSetSlot([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"), []byte(strconv.Itoa(slot)),
		[]byte("MIGRATING"), []byte(clusterNodeID(targetAddr)),
	}); err != nil { t.Fatal(err) }

	sourceConn, sourceReader := dialClusterClient(t, sourceAddr)
	writeClusterRESPCommand(t, sourceConn, "GET", key)
	wantAsk := fmt.Sprintf("-ASK %d %s\r\n", slot, targetAddr)
	if got := readRESPLine(t, sourceReader); got != wantAsk {
		t.Fatalf("source GET=%q want=%q", got, wantAsk)
	}

	targetConn, targetReader := dialClusterClient(t, targetAddr)
	writeClusterRESPCommand(t, targetConn, "ASKING")
	if got := readRESPLine(t, targetReader); got != "+OK\r\n" {
		t.Fatalf("ASKING=%q", got)
	}
	writeClusterRESPCommand(t, targetConn, "SET", key, "moved")
	if got := readRESPLine(t, targetReader); got != "+OK\r\n" {
		t.Fatalf("ASKING SET=%q", got)
	}

	writeClusterRESPCommand(t, targetConn, "GET", key)
	wantMoved := fmt.Sprintf("-MOVED %d %s\r\n", slot, sourceAddr)
	if got := readRESPLine(t, targetReader); got != wantMoved {
		t.Fatalf("ASKING should be consumed, GET=%q want=%q", got, wantMoved)
	}

	writeClusterRESPCommand(t, targetConn, "ASKING")
	if got := readRESPLine(t, targetReader); got != "+OK\r\n" { t.Fatalf("ASKING2=%q", got) }
	writeClusterRESPCommand(t, targetConn, "GET", key)
	if got := readRESPLine(t, targetReader); got != "$5\r\n" { t.Fatalf("GET header=%q", got) }
	if got := readRESPLine(t, targetReader); got != "moved\r\n" { t.Fatalf("GET value=%q", got) }
}

func TestClusterTCPClusterDownAfterCoverageLoss(t *testing.T) {
	tcp, err := Listen("127.0.0.1:0", engine.New())
	if err != nil { t.Fatal(err) }
	defer tcp.Close()

	local := tcp.listener.Addr().String()
	key := "coverage:lost"
	slot := clusterKeySlot([]byte(key))
	if err := tcp.server.configureClusterSlots(true, local, map[string]string{
		fmt.Sprintf("%d", slot): local,
	}); err != nil { t.Fatal(err) }

	tcp.server.clusterMu.Lock()
	tcp.server.clusterSlotOwners[slot] = ""
	tcp.server.clusterMu.Unlock()

	conn, reader := dialClusterClient(t, local)
	writeClusterRESPCommand(t, conn, "GET", key)
	if got := readRESPLine(t, reader); got != "-CLUSTERDOWN Hash slot not served\r\n" {
		t.Fatalf("GET=%q", got)
	}
}
