package server

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()

	replica, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(role), "*3\r\n$6\r\nmaster\r\n") {
		t.Fatalf("ROLE=%q", role)
	}
	info, err := s.Execute([][]byte{[]byte("INFO"), []byte("replication")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(info), "role:master\r\n") ||
		!strings.Contains(string(info), "connected_slaves:0\r\n") {
		t.Fatalf("INFO replication=%q", info)
	}
}

func TestReplicationPhase2PartialResyncAfterDisconnect(t *testing.T) {
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
	if err != nil {
		t.Fatal(err)
	}
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

func TestReplicationACKMonotonicAndInfo(t *testing.T) {
	s := New(engine.New())
	s.replication.init()

	id, _, _ := s.replication.registerReplica(func([]byte) error { return nil })
	defer s.replication.unregisterReplica(id)

	s.replication.acknowledgeReplica(id, 123)
	s.replication.acknowledgeReplica(id, 7)

	s.replication.mu.RLock()
	got := s.replication.replicaAckOffsets[id]
	s.replication.mu.RUnlock()
	if got != 123 {
		t.Fatalf("ack offset moved backwards: got=%d want=123", got)
	}

	info := s.replicationInfo()
	if !strings.Contains(info, "connected_slaves:1\r\n") {
		t.Fatalf("INFO replication missing connected slave: %q", info)
	}
	if !strings.Contains(info, "state=online,offset=123,lag=") {
		t.Fatalf("INFO replication missing replica offset/lag: %q", info)
	}
}

func TestDecodeRedisFullSyncRDB(t *testing.T) {
	appendDumpObject := func(dst []byte, key string, dump []byte) []byte {
		if len(dump) < 11 {
			t.Fatalf("short dump for %s", key)
		}
		dst = append(dst, dump[0])
		dst = appendRDBRawString(dst, []byte(key))
		dst = append(dst, dump[1:len(dump)-10]...)
		return dst
	}

	out := []byte("REDIS0012")
	out = append(out, redisRDBOpcodeAux)
	out = appendRDBRawString(out, []byte("redis-ver"))
	out = appendRDBRawString(out, []byte("8.2.9"))
	out = append(out, redisRDBOpcodeSelectDB)
	out = appendRDBLen(out, 0)
	out = append(out, redisRDBOpcodeResizeDB)
	out = appendRDBLen(out, 5)
	out = appendRDBLen(out, 1)

	stringDump, err := encodeKeyStringDump([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	out = appendDumpObject(out, "rdb:string", stringDump)

	out = append(out, redisRDBOpcodeExpireTimeMS)
	expiresAt := time.Now().Add(10 * time.Minute).UnixMilli()
	var expiry [8]byte
	binary.LittleEndian.PutUint64(expiry[:], uint64(expiresAt))
	out = append(out, expiry[:]...)
	ttlDump, err := encodeKeyStringDump([]byte("expires"))
	if err != nil {
		t.Fatal(err)
	}
	out = appendDumpObject(out, "rdb:ttl", ttlDump)

	hashDump, err := encodeHashDump([]engine.HashPair{
		{Field: []byte("a"), Value: []byte("1")},
		{Field: []byte("b"), Value: []byte("two")},
	})
	if err != nil {
		t.Fatal(err)
	}
	out = appendDumpObject(out, "rdb:hash", hashDump)

	setDump, err := encodeSetDump([][]byte{[]byte("1"), []byte("2"), []byte("3")})
	if err != nil {
		t.Fatal(err)
	}
	out = appendDumpObject(out, "rdb:set", setDump)

	listDump, err := encodeListDump([][]byte{[]byte("a"), []byte("b"), []byte("c")})
	if err != nil {
		t.Fatal(err)
	}
	out = appendDumpObject(out, "rdb:list", listDump)

	out = append(out, redisRDBOpcodeEOF)
	var checksum [8]byte
	binary.LittleEndian.PutUint64(checksum[:], redisCRC64(out))
	out = append(out, checksum[:]...)

	records, err := decodeRedisFullSyncRDB(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 6 || !records[0].Reset {
		t.Fatalf("records=%d reset=%v", len(records), len(records) > 0 && records[0].Reset)
	}

	store := engine.New()
	if err := store.Restore(records, true); err != nil {
		t.Fatal(err)
	}
	if value, found, wrong := store.GetString("rdb:string"); !found || wrong || string(value) != "hello" {
		t.Fatalf("string found=%v wrong=%v value=%q", found, wrong, value)
	}
	if pairs, err := store.HashGetAll("rdb:hash"); err != nil || len(pairs) != 2 {
		t.Fatalf("hash pairs=%v err=%v", pairs, err)
	}
	if members, err := store.SetMembers("rdb:set"); err != nil || len(members) != 3 {
		t.Fatalf("set members=%v err=%v", members, err)
	}
	if items, err := store.ListRange("rdb:list", 0, -1); err != nil || len(items) != 3 {
		t.Fatalf("list items=%v err=%v", items, err)
	}
	if ttl := store.TTL("rdb:ttl", true); ttl <= 0 {
		t.Fatalf("ttl=%d", ttl)
	}
}

func TestDecodeRedisFullSyncRDBRejectsChecksumCorruption(t *testing.T) {
	out := []byte("REDIS0012")
	out = append(out, redisRDBOpcodeSelectDB)
	out = appendRDBLen(out, 0)
	dump, err := encodeKeyStringDump([]byte("value"))
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, dump[0])
	out = appendRDBRawString(out, []byte("key"))
	out = append(out, dump[1:len(dump)-10]...)
	out = append(out, redisRDBOpcodeEOF)
	var checksum [8]byte
	binary.LittleEndian.PutUint64(checksum[:], redisCRC64(out))
	out = append(out, checksum[:]...)
	out[len(out)-9] ^= 0x01

	if _, err := decodeRedisFullSyncRDB(out); err == nil {
		t.Fatal("expected checksum error")
	}
}

func TestReadReplicationSnapshotEOFPreservesFollowingStream(t *testing.T) {
	marker := "0123456789abcdef0123456789abcdef01234567"
	payload := []byte("REDIS0012payload-0-not-the-marker")
	wire := []byte("$EOF:" + marker + "\r\n")
	wire = append(wire, payload...)
	wire = append(wire, []byte(marker)...)
	wire = append(wire, []byte("*2\r\n$4\r\nPING\r\n$4\r\nnext\r\n")...)

	reader := bufio.NewReaderSize(bytes.NewReader(wire), 16)
	got, isRedis, err := readReplicationSnapshot(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !isRedis {
		t.Fatal("expected Redis RDB snapshot")
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload=%q want=%q", got, payload)
	}

	args, _, err := readRedisReplicationCommand(reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 || string(args[0]) != "PING" || string(args[1]) != "next" {
		t.Fatalf("following command=%q", args)
	}
}

func TestReadReplicationSnapshotEOFRejectsBadMarkerLength(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("$EOF:short\r\nREDIS0012"))
	if _, _, err := readReplicationSnapshot(reader); err == nil {
		t.Fatal("expected marker length error")
	}
}

func TestAuthenticateReplicationUpstreamPassword(t *testing.T) {
	client, upstream := net.Pipe()
	defer client.Close()
	defer upstream.Close()

	done := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(upstream)
		args, _, err := readRedisReplicationCommand(reader)
		if err != nil {
			done <- err
			return
		}
		if len(args) != 2 || string(args[0]) != "AUTH" || string(args[1]) != "secret" {
			done <- fmt.Errorf("unexpected AUTH command: %q", args)
			return
		}
		_, err = upstream.Write([]byte("+OK\r\n"))
		done <- err
	}()

	reader := bufio.NewReader(client)
	if err := authenticateReplicationUpstream(client, reader, "", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAuthenticateReplicationUpstreamACL(t *testing.T) {
	client, upstream := net.Pipe()
	defer client.Close()
	defer upstream.Close()

	done := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(upstream)
		args, _, err := readRedisReplicationCommand(reader)
		if err != nil {
			done <- err
			return
		}
		if len(args) != 3 ||
			string(args[0]) != "AUTH" ||
			string(args[1]) != "replica-user" ||
			string(args[2]) != "replica-password" {
			done <- fmt.Errorf("unexpected AUTH command: %q", args)
			return
		}
		_, err = upstream.Write([]byte("+OK\r\n"))
		done <- err
	}()

	reader := bufio.NewReader(client)
	if err := authenticateReplicationUpstream(client, reader, "replica-user", "replica-password"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAuthenticateReplicationUpstreamFailureRedactsCredentials(t *testing.T) {
	client, upstream := net.Pipe()
	defer client.Close()
	defer upstream.Close()

	go func() {
		reader := bufio.NewReader(upstream)
		_, _, _ = readRedisReplicationCommand(reader)
		_, _ = upstream.Write([]byte("-WRONGPASS invalid username-password pair\r\n"))
	}()

	reader := bufio.NewReader(client)
	err := authenticateReplicationUpstream(client, reader, "private-user", "private-password")
	if err == nil {
		t.Fatal("expected authentication failure")
	}
	if strings.Contains(err.Error(), "private-user") || strings.Contains(err.Error(), "private-password") {
		t.Fatalf("credentials leaked in error: %v", err)
	}
}

func TestDialReplicationUpstreamTLSVerifiesCA(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()

	cert := upstream.Certificate()
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0600); err != nil {
		t.Fatal(err)
	}

	host, portText, err := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "https://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}

	serverName := ""
	if len(cert.DNSNames) > 0 {
		serverName = cert.DNSNames[0]
	} else if len(cert.IPAddresses) > 0 {
		serverName = cert.IPAddresses[0].String()
	} else {
		t.Fatal("test TLS certificate has no usable server identity")
	}

	s := New(engine.New())
	s.replicationMasterTLS = true
	s.replicationMasterTLSCA = caPath
	s.replicationMasterTLSSNI = serverName

	conn, err := s.dialReplicationUpstream(host, port)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
}

func TestDialReplicationUpstreamTLSRejectsInvalidCA(t *testing.T) {
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caPath, []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}

	s := New(engine.New())
	s.replicationMasterTLS = true
	s.replicationMasterTLSCA = caPath

	_, err := s.dialReplicationUpstream("127.0.0.1", 1)
	if err == nil || !strings.Contains(err.Error(), "no valid certificates") {
		t.Fatalf("expected invalid CA rejection, got %v", err)
	}
}
