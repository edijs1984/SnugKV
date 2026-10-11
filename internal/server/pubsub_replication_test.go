package server

import (
	"bufio"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func TestPublishReachesSubscribersOnReplica(t *testing.T) {
	primary, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()
	replica, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer replica.Close()

	paddr := primary.listener.Addr().(*net.TCPAddr)
	if _, err := replica.server.Execute([][]byte{[]byte("REPLICAOF"), []byte("127.0.0.1"), []byte(strconv.Itoa(paddr.Port))}); err != nil {
		t.Fatal(err)
	}
	waitReplication(t, func() bool { return replica.server.replication.snapshot().masterLinkStatus == "up" })

	conn, err := net.Dial("tcp", replica.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := bufio.NewReader(conn)
	if _, err := conn.Write([]byte("*2\r\n$9\r\nSUBSCRIBE\r\n$4\r\nnews\r\n")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ { // the subscribe confirmation is six lines
		if _, err := r.ReadString('\n'); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := primary.server.Execute([][]byte{[]byte("PUBLISH"), []byte("news"), []byte("hello")}); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var got []string
	for len(got) < 7 {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v (got %q)", err, got)
		}
		got = append(got, strings.TrimSpace(line))
	}
	if got[2] != "message" || got[4] != "news" || got[6] != "hello" {
		t.Fatalf("message = %q", got)
	}

	// Publishing on the replica itself reaches its own subscribers but must not
	// disturb the replication offset it shares with the primary.
	before := replica.server.replication.snapshot().offset
	if _, err := replica.server.Execute([][]byte{[]byte("PUBLISH"), []byte("news"), []byte("local")}); err != nil {
		t.Fatal(err)
	}
	if after := replica.server.replication.snapshot().offset; after != before {
		t.Fatalf("replica offset moved %d -> %d", before, after)
	}
}
