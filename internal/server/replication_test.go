package server

import (
	"net"
	"strings"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func waitReplication(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("replication condition timed out")
}

func TestReplicationPhase1FullSyncLiveWritesAndPromotion(t *testing.T) {
	primary, err := Listen("127.0.0.1:0", engine.New())
	if err != nil { t.Fatal(err) }
	defer primary.Close()

	replica, err := Listen("127.0.0.1:0", engine.New())
	if err != nil { t.Fatal(err) }
	defer replica.Close()

	if _, err := primary.server.Execute([][]byte{[]byte("SET"), []byte("initial"), []byte("alpha")}); err != nil {
		t.Fatal(err)
	}
	if _, err := primary.server.Execute([][]byte{[]byte("HSET"), []byte("hash"), []byte("a"), []byte("1"), []byte("b"), []byte("2")}); err != nil {
		t.Fatal(err)
	}
	if _, err := primary.server.Execute([][]byte{[]byte("SET"), []byte("expiring"), []byte("value"), []byte("PX"), []byte("60000")}); err != nil {
		t.Fatal(err)
	}

	addr := primary.listener.Addr().(*net.TCPAddr)
	if _, err := replica.server.Execute([][]byte{
		[]byte("REPLICAOF"), []byte("127.0.0.1"), []byte(strings.TrimSpace(strings.Split(addr.String(), ":")[len(strings.Split(addr.String(), ":"))-1])),
	}); err != nil {
		t.Fatal(err)
	}

	waitReplication(t, func() bool {
		state := replica.server.replication.snapshot()
		return state.masterLinkStatus == "up"
	})
	waitReplication(t, func() bool {
		v, found, wrong := replica.server.store.GetString("initial")
		return found && !wrong && string(v) == "alpha"
	})

	if n := primary.server.replication.snapshot().connectedReplicas; n != 1 {
		t.Fatalf("connected replicas=%d", n)
	}

	if _, err := primary.server.Execute([][]byte{[]byte("SET"), []byte("live"), []byte("beta")}); err != nil {
		t.Fatal(err)
	}
	if _, err := primary.server.Execute([][]byte{[]byte("INCR"), []byte("counter")}); err != nil {
		t.Fatal(err)
	}
	if _, err := primary.server.Execute([][]byte{[]byte("HSET"), []byte("hash"), []byte("c"), []byte("3")}); err != nil {
		t.Fatal(err)
	}

	waitReplication(t, func() bool {
		v, found, wrong := replica.server.store.GetString("live")
		return found && !wrong && string(v) == "beta"
	})
	waitReplication(t, func() bool {
		v, found, wrong := replica.server.store.GetString("counter")
		return found && !wrong && string(v) == "1"
	})

	if _, err := replica.server.Execute([][]byte{[]byte("SET"), []byte("forbidden"), []byte("x")}); err == nil ||
		err.Error() != "READONLY You can't write against a read only replica." {
		t.Fatalf("write on replica err=%v", err)
	}

	if _, err := replica.server.Execute([][]byte{[]byte("REPLICAOF"), []byte("NO"), []byte("ONE")}); err != nil {
		t.Fatal(err)
	}
	if state := replica.server.replication.snapshot(); state.role != replicationMaster {
		t.Fatalf("role after detach=%v", state.role)
	}
	if _, err := replica.server.Execute([][]byte{[]byte("SET"), []byte("after-detach"), []byte("ok")}); err != nil {
		t.Fatal(err)
	}
}

func TestReplicationRoleAndInfoShape(t *testing.T) {
	s := New(engine.New())
	role, err := s.Execute([][]byte{[]byte("ROLE")})
	if err != nil { t.Fatal(err) }
	if !strings.HasPrefix(string(role), "*3\r\n$6\r\nmaster\r\n") {
		t.Fatalf("ROLE=%q", role)
	}
	info, err := s.Execute([][]byte{[]byte("INFO"), []byte("replication")})
	if err != nil { t.Fatal(err) }
	if !strings.Contains(string(info), "role:master\r\n") ||
		!strings.Contains(string(info), "connected_slaves:0\r\n") {
		t.Fatalf("INFO replication=%q", info)
	}
}
