package server

import (
	"net"
	"strconv"
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
		[]byte("REPLICAOF"), []byte("127.0.0.1"), []byte(strconv.Itoa(addr.Port)),
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


func TestReplicationPhase2PartialResyncAfterDisconnect(t *testing.T) {
	primary, err := Listen("127.0.0.1:0", engine.New())
	if err != nil { t.Fatal(err) }
	defer primary.Close()

	replica, err := Listen("127.0.0.1:0", engine.New())
	if err != nil { t.Fatal(err) }
	defer replica.Close()

	addr := primary.listener.Addr().(*net.TCPAddr)
	host := "127.0.0.1"
	port := addr.Port

	if _, err := replica.server.Execute([][]byte{
		[]byte("REPLICAOF"), []byte(host), []byte(strconv.Itoa(port)),
	}); err != nil {
		t.Fatal(err)
	}

	waitReplication(t, func() bool {
		return replica.server.replication.snapshot().masterLinkStatus == "up"
	})
	waitReplication(t, func() bool {
		return primary.server.replication.snapshot().connectedReplicas == 1
	})

	if _, err := primary.server.Execute([][]byte{[]byte("SET"), []byte("before-gap"), []byte("1")}); err != nil {
		t.Fatal(err)
	}
	waitReplication(t, func() bool {
		v, found, wrong := replica.server.store.GetString("before-gap")
		return found && !wrong && string(v) == "1"
	})

	before := replica.server.replication.snapshot()
	replica.server.replication.mu.RLock()
	masterRunID := replica.server.replication.masterRunID
	replica.server.replication.mu.RUnlock()
	if masterRunID == "" || before.offset <= 0 {
		t.Fatalf("missing saved PSYNC state runid=%q offset=%d", masterRunID, before.offset)
	}

	replica.server.stopReplicaFollow()
	waitReplication(t, func() bool {
		return primary.server.replication.snapshot().connectedReplicas == 0
	})

	// A full resync begins with Reset and would remove this sentinel. A successful
	// partial resync must leave it intact while replaying only the backlog gap.
	if err := replica.server.store.SetPlain("partial-sentinel", []byte("keep")); err != nil {
		t.Fatal(err)
	}

	if _, err := primary.server.Execute([][]byte{[]byte("SET"), []byte("during-gap-a"), []byte("A")}); err != nil {
		t.Fatal(err)
	}
	if _, err := primary.server.Execute([][]byte{[]byte("INCR"), []byte("during-gap-counter")}); err != nil {
		t.Fatal(err)
	}

	state := primary.server.replication.snapshot()
	if !state.backlogActive || state.backlogBytes <= 0 {
		t.Fatalf("backlog state active=%v bytes=%d", state.backlogActive, state.backlogBytes)
	}

	replica.server.startReplicaFollow(host, port)
	waitReplication(t, func() bool {
		return replica.server.replication.snapshot().masterLinkStatus == "up"
	})
	waitReplication(t, func() bool {
		v, found, wrong := replica.server.store.GetString("during-gap-a")
		return found && !wrong && string(v) == "A"
	})
	waitReplication(t, func() bool {
		v, found, wrong := replica.server.store.GetString("during-gap-counter")
		return found && !wrong && string(v) == "1"
	})

	v, found, wrong := replica.server.store.GetString("partial-sentinel")
	if !found || wrong || string(v) != "keep" {
		t.Fatalf("partial-resync sentinel found=%v wrong=%v value=%q", found, wrong, v)
	}
}

func TestReplicationPhase2InfoBacklogShape(t *testing.T) {
	s := New(engine.New())

	info, err := s.Execute([][]byte{[]byte("INFO"), []byte("replication")})
	if err != nil { t.Fatal(err) }
	for _, want := range []string{
		"repl_backlog_active:0\r\n",
		"repl_backlog_size:",
		"repl_backlog_first_byte_offset:",
		"repl_backlog_histlen:0\r\n",
	} {
		if !strings.Contains(string(info), want) {
			t.Fatalf("INFO replication missing %q: %q", want, info)
		}
	}
}
