package server

import (
	"bufio"
	"io"
	"errors"
	"bytes"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"math"
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
	"snugkv/internal/persistence"
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

func TestEncodePlainSetReplicationFrameRoundTrip(t *testing.T) {
	key := []byte{0x00, 0xff, 'k', 'e', 'y'}
	value := []byte{0x01, 0x02, 0xfe, 'v', 'a', 'l', 'u', 'e'}

	frame := encodePlainSetReplicationFrame(key, value)
	records, err := decodeReplicationFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records=%d want=1", len(records))
	}
	if !bytes.Equal(records[0].Key, key) {
		t.Fatalf("key=%v want=%v", records[0].Key, key)
	}
	if !bytes.Equal(records[0].Value, value) {
		t.Fatalf("value=%v want=%v", records[0].Value, value)
	}
	if records[0].Deleted || records[0].ExpiresAtMS != 0 || records[0].ValueType != 0 {
		t.Fatalf("unexpected metadata: %+v", records[0])
	}
}

func TestReplicationPlainSetDirectRecordClearsReplicaTTL(t *testing.T) {
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

	if _, err := primary.server.Execute([][]byte{
		[]byte("SET"), []byte("ttl-key"), []byte("old"), []byte("PX"), []byte("60000"),
	}); err != nil {
		t.Fatal(err)
	}

	addr := primary.listener.Addr().(*net.TCPAddr)
	if _, err := replica.server.Execute([][]byte{
		[]byte("REPLICAOF"), []byte("127.0.0.1"), []byte(strconv.Itoa(addr.Port)),
	}); err != nil {
		t.Fatal(err)
	}

	waitReplication(t, func() bool {
		return replica.server.replication.snapshot().masterLinkStatus == "up"
	})
	waitReplication(t, func() bool {
		return replica.server.store.TTL("ttl-key", true) > 0
	})

	if _, err := primary.server.Execute([][]byte{
		[]byte("SET"), []byte("ttl-key"), []byte("new"),
	}); err != nil {
		t.Fatal(err)
	}

	waitReplication(t, func() bool {
		value, found, wrong := replica.server.store.GetString("ttl-key")
		return found && !wrong && string(value) == "new"
	})
	if ttl := replica.server.store.TTL("ttl-key", true); ttl != -1 {
		t.Fatalf("replica ttl=%d want=-1 after plain SET", ttl)
	}
}

func TestReplicationPublishDoesNotWaitForReplicaSocket(t *testing.T) {
	s := New(engine.New())

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	id, _, _ := s.replication.registerReplica(func([]byte) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return nil
	})
	defer func() {
		close(release)
		s.replication.unregisterReplica(id)
	}()

	s.publishReplication([]persistence.Record{{Reset: true}})

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("replica writer did not receive first payload")
	}

	done := make(chan struct{})
	go func() {
		s.publishReplication([]persistence.Record{{Reset: true}})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("publishReplication waited for replica socket write")
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

func testRedisZiplist(t *testing.T, values ...string) []byte {
	t.Helper()
	raw := make([]byte, 10)
	var prevLen int
	tail := 10
	for i, value := range values {
		if len(value) > 63 {
			t.Fatalf("test ziplist value too long: %d", len(value))
		}
		entryStart := len(raw)
		if i == len(values)-1 {
			tail = entryStart
		}
		if prevLen >= 254 {
			raw = append(raw, 254)
			var buf [4]byte
			binary.LittleEndian.PutUint32(buf[:], uint32(prevLen))
			raw = append(raw, buf[:]...)
		} else {
			raw = append(raw, byte(prevLen))
		}
		raw = append(raw, byte(len(value)))
		raw = append(raw, []byte(value)...)
		prevLen = len(raw) - entryStart
	}
	raw = append(raw, 0xff)
	binary.LittleEndian.PutUint32(raw[:4], uint32(len(raw)))
	binary.LittleEndian.PutUint32(raw[4:8], uint32(tail))
	binary.LittleEndian.PutUint16(raw[8:10], uint16(len(values)))
	return raw
}

func testRedisZipmap(t *testing.T, pairs ...[2]string) []byte {
	t.Helper()
	if len(pairs) >= 254 {
		t.Fatal("test zipmap helper only supports short count")
	}
	raw := []byte{byte(len(pairs))}
	for _, pair := range pairs {
		if len(pair[0]) >= 254 || len(pair[1]) >= 254 {
			t.Fatal("test zipmap helper only supports short strings")
		}
		raw = append(raw, byte(len(pair[0])))
		raw = append(raw, []byte(pair[0])...)
		raw = append(raw, byte(len(pair[1])))
		raw = append(raw, 0)
		raw = append(raw, []byte(pair[1])...)
	}
	return append(raw, 255)
}

func TestDecodeRedisLegacyZipmap(t *testing.T) {
	raw := testRedisZipmap(t, [2]string{"a", "1"}, [2]string{"b", "two"})
	body := appendRDBRawString(nil, raw)
	pos := 0
	obj, err := decodeRedisRDBObjectAt(body, &pos, redisRDBTypeHashZipmap)
	if err != nil {
		t.Fatal(err)
	}
	if pos != len(body) || len(obj.hash) != 2 ||
		string(obj.hash[0].Field) != "a" || string(obj.hash[0].Value) != "1" ||
		string(obj.hash[1].Field) != "b" || string(obj.hash[1].Value) != "two" {
		t.Fatalf("unexpected hash zipmap: pos=%d len=%d hash=%v", pos, len(body), obj.hash)
	}

	raw = testRedisZipmap(t, [2]string{"a", "1"})
	raw[0] = 2
	if _, err := decodeRedisZipmap(raw); err == nil {
		t.Fatal("expected zipmap entry count mismatch")
	}
}

func TestDecodeRedisLegacyZiplists(t *testing.T) {
	t.Run("list ziplist", func(t *testing.T) {
		raw := testRedisZiplist(t, "a", "b", "c")
		body := appendRDBRawString(nil, raw)
		pos := 0
		obj, err := decodeRedisRDBObjectAt(body, &pos, redisRDBTypeListZiplist)
		if err != nil {
			t.Fatal(err)
		}
		if pos != len(body) || len(obj.list) != 3 ||
			string(obj.list[0]) != "a" || string(obj.list[2]) != "c" {
			t.Fatalf("unexpected list ziplist: pos=%d len=%d list=%q", pos, len(body), obj.list)
		}
	})

	t.Run("hash ziplist", func(t *testing.T) {
		raw := testRedisZiplist(t, "a", "1", "b", "two")
		body := appendRDBRawString(nil, raw)
		pos := 0
		obj, err := decodeRedisRDBObjectAt(body, &pos, redisRDBTypeHashZiplist)
		if err != nil {
			t.Fatal(err)
		}
		if pos != len(body) || len(obj.hash) != 2 ||
			string(obj.hash[0].Field) != "a" || string(obj.hash[0].Value) != "1" ||
			string(obj.hash[1].Field) != "b" || string(obj.hash[1].Value) != "two" {
			t.Fatalf("unexpected hash ziplist: pos=%d len=%d hash=%v", pos, len(body), obj.hash)
		}
	})

	t.Run("zset ziplist", func(t *testing.T) {
		raw := testRedisZiplist(t, "one", "1.5", "two", "-2.25")
		body := appendRDBRawString(nil, raw)
		pos := 0
		obj, err := decodeRedisRDBObjectAt(body, &pos, redisRDBTypeZSetZiplist)
		if err != nil {
			t.Fatal(err)
		}
		if pos != len(body) || len(obj.zset) != 2 ||
			string(obj.zset[0].Member) != "one" || obj.zset[0].Score != 1.5 ||
			string(obj.zset[1].Member) != "two" || obj.zset[1].Score != -2.25 {
			t.Fatalf("unexpected zset ziplist: pos=%d len=%d zset=%v", pos, len(body), obj.zset)
		}
	})

	t.Run("legacy quicklist", func(t *testing.T) {
		body := appendRDBLen(nil, 2)
		body = appendRDBRawString(body, testRedisZiplist(t, "a", "b"))
		body = appendRDBRawString(body, testRedisZiplist(t, "c", "d"))
		pos := 0
		obj, err := decodeRedisRDBObjectAt(body, &pos, redisRDBTypeListQuicklist)
		if err != nil {
			t.Fatal(err)
		}
		if pos != len(body) || len(obj.list) != 4 ||
			string(obj.list[0]) != "a" || string(obj.list[3]) != "d" {
			t.Fatalf("unexpected legacy quicklist: pos=%d len=%d list=%q", pos, len(body), obj.list)
		}
	})

	t.Run("ziplist corruption", func(t *testing.T) {
		raw := testRedisZiplist(t, "a", "b")
		raw[10] = 1
		if _, err := decodeRedisZiplist(raw); err == nil {
			t.Fatal("expected invalid first prevlen rejection")
		}
	})
}

func TestDecodeRedisRDBHashFieldExpirationMetadata(t *testing.T) {
	minExpire := time.Now().Add(2 * time.Minute).UnixMilli()

	body := make([]byte, 8)
	binary.LittleEndian.PutUint64(body[:8], uint64(minExpire))
	body = appendRDBLen(body, 3)

	// Field a expires exactly at minExpire: relative TTL is 1.
	body = appendRDBLen(body, 1)
	body = appendRDBRawString(body, []byte("a"))
	body = appendRDBRawString(body, []byte("one"))

	// Field b has no TTL.
	body = appendRDBLen(body, 0)
	body = appendRDBRawString(body, []byte("b"))
	body = appendRDBRawString(body, []byte("two"))

	// Field c expires 5 seconds after minExpire: delta + 1.
	body = appendRDBLen(body, 5001)
	body = appendRDBRawString(body, []byte("c"))
	body = appendRDBRawString(body, []byte("three"))

	pos := 0
	obj, err := decodeRedisRDBObjectAt(body, &pos, redisRDBTypeHashMetadata)
	if err != nil {
		t.Fatal(err)
	}
	if pos != len(body) || len(obj.hash) != 3 {
		t.Fatalf("decode pos=%d len=%d pairs=%v", pos, len(body), obj.hash)
	}
	if obj.hash[0].ExpiresAtMS != minExpire ||
		obj.hash[1].ExpiresAtMS != 0 ||
		obj.hash[2].ExpiresAtMS != minExpire+5000 {
		t.Fatalf("unexpected field expiries: %+v", obj.hash)
	}

	record, err := buildRestoreRecord("rdb:hfe", obj, 0)
	if err != nil {
		t.Fatal(err)
	}
	store := engine.New()
	if err := store.Restore([]persistence.Record{record}, true); err != nil {
		t.Fatal(err)
	}
	ttl, err := store.HashFieldPTTL("rdb:hfe", [][]byte{[]byte("a"), []byte("b"), []byte("c")})
	if err != nil {
		t.Fatal(err)
	}
	if len(ttl) != 3 || ttl[0] <= 0 || ttl[1] != -1 || ttl[2] <= ttl[0] {
		t.Fatalf("restored field TTLs = %v", ttl)
	}
}

func TestDecodeRedisRDBHashFieldExpirationMetadataPreGA(t *testing.T) {
	expireAt := time.Now().Add(2 * time.Minute).UnixMilli()
	body := appendRDBLen(nil, 1)
	body = appendRDBLen(body, uint64(expireAt))
	body = appendRDBRawString(body, []byte("field"))
	body = appendRDBRawString(body, []byte("value"))

	pos := 0
	obj, err := decodeRedisRDBObjectAt(body, &pos, redisRDBTypeHashMetadataPreGA)
	if err != nil {
		t.Fatal(err)
	}
	if pos != len(body) || len(obj.hash) != 1 || obj.hash[0].ExpiresAtMS != expireAt {
		t.Fatalf("unexpected PRE_GA decode: pos=%d len=%d hash=%+v", pos, len(body), obj.hash)
	}
}

func TestDecodeRedisRDBHashFieldExpirationListpack(t *testing.T) {
	expireA := time.Now().Add(2 * time.Minute).UnixMilli()
	expireC := expireA + 5000
	lp, err := encodeRedisListpack([][]byte{
		[]byte("a"), []byte("one"), []byte(strconv.FormatInt(expireA, 10)),
		[]byte("c"), []byte("three"), []byte(strconv.FormatInt(expireC, 10)),
		[]byte("b"), []byte("two"), []byte("0"),
	})
	if err != nil {
		t.Fatal(err)
	}

	body := make([]byte, 8)
	binary.LittleEndian.PutUint64(body, uint64(expireA))
	body = appendRDBRawString(body, lp)

	pos := 0
	obj, err := decodeRedisRDBObjectAt(body, &pos, redisRDBTypeHashListpackEx)
	if err != nil {
		t.Fatal(err)
	}
	if pos != len(body) || len(obj.hash) != 3 {
		t.Fatalf("decode pos=%d len=%d hash=%+v", pos, len(body), obj.hash)
	}
	if string(obj.hash[0].Field) != "a" || obj.hash[0].ExpiresAtMS != expireA ||
		string(obj.hash[1].Field) != "c" || obj.hash[1].ExpiresAtMS != expireC ||
		string(obj.hash[2].Field) != "b" || obj.hash[2].ExpiresAtMS != 0 {
		t.Fatalf("unexpected LISTPACK_EX decode: %+v", obj.hash)
	}

	record, err := buildRestoreRecord("rdb:hfe:lp", obj, 0)
	if err != nil {
		t.Fatal(err)
	}
	store := engine.New()
	if err := store.Restore([]persistence.Record{record}, true); err != nil {
		t.Fatal(err)
	}
	ttl, err := store.HashFieldPTTL("rdb:hfe:lp", [][]byte{[]byte("a"), []byte("b"), []byte("c")})
	if err != nil {
		t.Fatal(err)
	}
	if len(ttl) != 3 || ttl[0] <= 0 || ttl[1] != -1 || ttl[2] <= ttl[0] {
		t.Fatalf("restored LISTPACK_EX field TTLs = %v", ttl)
	}
}

func TestDecodeRedisRDBHashFieldExpirationListpackPreGA(t *testing.T) {
	expireAt := time.Now().Add(2 * time.Minute).UnixMilli()
	lp, err := encodeRedisListpack([][]byte{
		[]byte("field"), []byte("value"), []byte(strconv.FormatInt(expireAt, 10)),
	})
	if err != nil {
		t.Fatal(err)
	}
	body := appendRDBRawString(nil, lp)

	pos := 0
	obj, err := decodeRedisRDBObjectAt(body, &pos, redisRDBTypeHashListpackExPreGA)
	if err != nil {
		t.Fatal(err)
	}
	if pos != len(body) || len(obj.hash) != 1 || obj.hash[0].ExpiresAtMS != expireAt {
		t.Fatalf("unexpected LISTPACK_EX PRE_GA decode: pos=%d len=%d hash=%+v", pos, len(body), obj.hash)
	}
}

func TestDecodeRedisRDBHashFieldExpirationListpackRejectsCorruption(t *testing.T) {
	t.Run("tuple count", func(t *testing.T) {
		lp, err := encodeRedisListpack([][]byte{[]byte("a"), []byte("one")})
		if err != nil {
			t.Fatal(err)
		}
		body := appendRDBRawString(nil, lp)
		pos := 0
		if _, err := decodeRedisRDBObjectAt(body, &pos, redisRDBTypeHashListpackExPreGA); err == nil {
			t.Fatal("expected invalid LISTPACK_EX tuple count")
		}
	})

	t.Run("negative ttl", func(t *testing.T) {
		lp, err := encodeRedisListpack([][]byte{[]byte("a"), []byte("one"), []byte("-1")})
		if err != nil {
			t.Fatal(err)
		}
		body := appendRDBRawString(nil, lp)
		pos := 0
		if _, err := decodeRedisRDBObjectAt(body, &pos, redisRDBTypeHashListpackExPreGA); err == nil {
			t.Fatal("expected negative LISTPACK_EX TTL rejection")
		}
	})

	t.Run("duplicate field", func(t *testing.T) {
		lp, err := encodeRedisListpack([][]byte{
			[]byte("a"), []byte("one"), []byte("0"),
			[]byte("a"), []byte("two"), []byte("0"),
		})
		if err != nil {
			t.Fatal(err)
		}
		body := appendRDBRawString(nil, lp)
		pos := 0
		if _, err := decodeRedisRDBObjectAt(body, &pos, redisRDBTypeHashListpackExPreGA); err == nil {
			t.Fatal("expected duplicate LISTPACK_EX field rejection")
		}
	})
}

func TestDecodeRedisRDBHashTemplateFormats(t *testing.T) {
	fields := [][]byte{[]byte("a"), []byte("b")}
	templates := map[uint64]redisRDBHashTemplate{
		7: {fields: fields},
	}

	t.Run("tmpl lp self contained", func(t *testing.T) {
		body := appendRDBLen(nil, 1)
		body = appendRDBLen(body, uint64(len(fields)))
		for _, field := range fields {
			body = appendRDBRawString(body, field)
		}
		lp, err := encodeRedisListpack([][]byte{
			[]byte("7"), []byte("one"), []byte("two"),
		})
		if err != nil {
			t.Fatal(err)
		}
		body = appendRDBRawString(body, lp)

		pos := 0
		obj, err := decodeRedisRDBObjectAtWithTemplates(body, &pos, redisRDBTypeHashTmplLP, nil)
		if err != nil {
			t.Fatal(err)
		}
		if pos != len(body) || len(obj.hash) != 2 ||
			string(obj.hash[0].Field) != "a" || string(obj.hash[0].Value) != "one" ||
			string(obj.hash[1].Field) != "b" || string(obj.hash[1].Value) != "two" {
			t.Fatalf("unexpected TMPL_LP decode: pos=%d len=%d hash=%+v", pos, len(body), obj.hash)
		}
	})

	t.Run("tmpl lp ref", func(t *testing.T) {
		lp, err := encodeRedisListpack([][]byte{
			[]byte("7"), []byte("one"), []byte("two"),
		})
		if err != nil {
			t.Fatal(err)
		}
		body := appendRDBRawString(nil, lp)

		pos := 0
		obj, err := decodeRedisRDBObjectAtWithTemplates(body, &pos, redisRDBTypeHashTmplLPRef, templates)
		if err != nil {
			t.Fatal(err)
		}
		if pos != len(body) || len(obj.hash) != 2 ||
			string(obj.hash[0].Field) != "a" || string(obj.hash[0].Value) != "one" ||
			string(obj.hash[1].Field) != "b" || string(obj.hash[1].Value) != "two" {
			t.Fatalf("unexpected TMPL_LP_REF decode: pos=%d len=%d hash=%+v", pos, len(body), obj.hash)
		}
	})

	t.Run("tmpl array self contained", func(t *testing.T) {
		body := appendRDBLen(nil, 1)
		body = appendRDBLen(body, uint64(len(fields)))
		for _, field := range fields {
			body = appendRDBRawString(body, field)
		}
		body = appendRDBRawString(body, []byte("one"))
		body = appendRDBRawString(body, []byte("two"))

		pos := 0
		obj, err := decodeRedisRDBObjectAtWithTemplates(body, &pos, redisRDBTypeHashTmplArray, nil)
		if err != nil {
			t.Fatal(err)
		}
		if pos != len(body) || len(obj.hash) != 2 ||
			string(obj.hash[0].Field) != "a" || string(obj.hash[0].Value) != "one" ||
			string(obj.hash[1].Field) != "b" || string(obj.hash[1].Value) != "two" {
			t.Fatalf("unexpected TMPL_ARRAY decode: pos=%d len=%d hash=%+v", pos, len(body), obj.hash)
		}
	})

	t.Run("tmpl array ref", func(t *testing.T) {
		body := appendRDBLen(nil, 7)
		body = appendRDBRawString(body, []byte("one"))
		body = appendRDBRawString(body, []byte("two"))

		pos := 0
		obj, err := decodeRedisRDBObjectAtWithTemplates(body, &pos, redisRDBTypeHashTmplArrayRef, templates)
		if err != nil {
			t.Fatal(err)
		}
		if pos != len(body) || len(obj.hash) != 2 ||
			string(obj.hash[0].Field) != "a" || string(obj.hash[0].Value) != "one" ||
			string(obj.hash[1].Field) != "b" || string(obj.hash[1].Value) != "two" {
			t.Fatalf("unexpected TMPL_ARRAY_REF decode: pos=%d len=%d hash=%+v", pos, len(body), obj.hash)
		}
	})
}

func TestValidateRedisRDBTemplateFieldsUsesRedisLengthThenBytesOrder(t *testing.T) {
	if err := validateRedisRDBTemplateFields([][]byte{
		[]byte("age"),
		[]byte("name"),
		[]byte("email"),
	}); err != nil {
		t.Fatalf("valid Redis template order rejected: %v", err)
	}

	if err := validateRedisRDBTemplateFields([][]byte{
		[]byte("age"),
		[]byte("email"),
		[]byte("name"),
	}); err == nil {
		t.Fatal("expected invalid Redis template order")
	}
}

func TestDecodeRedisRDBHashTemplateRejectsInvalidRefs(t *testing.T) {
	t.Run("unknown id", func(t *testing.T) {
		body := appendRDBLen(nil, 99)
		body = appendRDBRawString(body, []byte("value"))
		pos := 0
		if _, err := decodeRedisRDBObjectAtWithTemplates(body, &pos, redisRDBTypeHashTmplArrayRef, map[uint64]redisRDBHashTemplate{}); err == nil {
			t.Fatal("expected unknown template id rejection")
		}
	})

	t.Run("unsorted fields", func(t *testing.T) {
		body := appendRDBLen(nil, 1)
		body = appendRDBLen(body, 2)
		body = appendRDBRawString(body, []byte("b"))
		body = appendRDBRawString(body, []byte("a"))
		body = appendRDBRawString(body, []byte("one"))
		body = appendRDBRawString(body, []byte("two"))
		pos := 0
		if _, err := decodeRedisRDBObjectAtWithTemplates(body, &pos, redisRDBTypeHashTmplArray, nil); err == nil {
			t.Fatal("expected unsorted template field rejection")
		}
	})
}

func TestDecodeRedisFullSyncRDBHashTemplateRegistry(t *testing.T) {
	rdb := []byte("REDIS0016")

	rdb = append(rdb, redisRDBOpcodeHashTemplate)
	rdb = appendRDBLen(rdb, 7)
	rdb = appendRDBLen(rdb, 2)
	rdb = appendRDBRawString(rdb, []byte("a"))
	rdb = appendRDBRawString(rdb, []byte("b"))

	rdb = append(rdb, redisRDBOpcodeSelectDB)
	rdb = appendRDBLen(rdb, 0)
	rdb = append(rdb, redisRDBOpcodeResizeDB)
	rdb = appendRDBLen(rdb, 2)
	rdb = appendRDBLen(rdb, 0)

	lp, err := encodeRedisListpack([][]byte{
		[]byte("7"), []byte("one"), []byte("two"),
	})
	if err != nil {
		t.Fatal(err)
	}
	rdb = append(rdb, redisRDBTypeHashTmplLPRef)
	rdb = appendRDBRawString(rdb, []byte("tmpl:lp"))
	rdb = appendRDBRawString(rdb, lp)

	rdb = append(rdb, redisRDBTypeHashTmplArrayRef)
	rdb = appendRDBRawString(rdb, []byte("tmpl:array"))
	rdb = appendRDBLen(rdb, 7)
	rdb = appendRDBRawString(rdb, []byte("three"))
	rdb = appendRDBRawString(rdb, []byte("four"))

	rdb = append(rdb, redisRDBOpcodeEOF)
	var checksum [8]byte
	binary.LittleEndian.PutUint64(checksum[:], redisCRC64(rdb))
	rdb = append(rdb, checksum[:]...)

	records, err := decodeRedisFullSyncRDB(rdb)
	if err != nil {
		t.Fatal(err)
	}
	store := engine.New()
	if err := store.Restore(records, true); err != nil {
		t.Fatal(err)
	}

	got, err := store.HashGetAll("tmpl:lp")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got[0].Field) != "a" || string(got[0].Value) != "one" ||
		string(got[1].Field) != "b" || string(got[1].Value) != "two" {
		t.Fatalf("tmpl:lp = %+v", got)
	}

	got, err = store.HashGetAll("tmpl:array")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got[0].Field) != "a" || string(got[0].Value) != "three" ||
		string(got[1].Field) != "b" || string(got[1].Value) != "four" {
		t.Fatalf("tmpl:array = %+v", got)
	}
}

func TestDecodeRedisRDBPlainCollections(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		body := appendRDBLen(nil, 3)
		body = appendRDBRawString(body, []byte("a"))
		var ok bool
		body, ok = appendRDBIntegerString(body, []byte("2"))
		if !ok {
			t.Fatal("expected integer encoding")
		}
		body = appendRDBRawString(body, []byte("c"))

		pos := 0
		obj, err := decodeRedisRDBObjectAt(body, &pos, redisRDBTypeList)
		if err != nil {
			t.Fatal(err)
		}
		if pos != len(body) || len(obj.list) != 3 ||
			string(obj.list[0]) != "a" ||
			string(obj.list[1]) != "2" ||
			string(obj.list[2]) != "c" {
			t.Fatalf("unexpected list decode: pos=%d len=%d values=%q", pos, len(body), obj.list)
		}
	})

	t.Run("set", func(t *testing.T) {
		body := appendRDBLen(nil, 3)
		body = appendRDBRawString(body, []byte("alpha"))
		body = appendRDBRawString(body, []byte("beta"))
		body = appendRDBRawString(body, []byte("gamma"))

		pos := 0
		obj, err := decodeRedisRDBObjectAt(body, &pos, redisRDBTypeSet)
		if err != nil {
			t.Fatal(err)
		}
		if pos != len(body) || len(obj.set) != 3 {
			t.Fatalf("unexpected set decode: pos=%d len=%d members=%q", pos, len(body), obj.set)
		}
	})

	t.Run("hash", func(t *testing.T) {
		body := appendRDBLen(nil, 2)
		body = appendRDBRawString(body, []byte("a"))
		body = appendRDBRawString(body, []byte("1"))
		body = appendRDBRawString(body, []byte("b"))
		body = appendRDBRawString(body, []byte("two"))

		pos := 0
		obj, err := decodeRedisRDBObjectAt(body, &pos, redisRDBTypeHash)
		if err != nil {
			t.Fatal(err)
		}
		if pos != len(body) || len(obj.hash) != 2 ||
			string(obj.hash[0].Field) != "a" ||
			string(obj.hash[0].Value) != "1" ||
			string(obj.hash[1].Field) != "b" ||
			string(obj.hash[1].Value) != "two" {
			t.Fatalf("unexpected hash decode: pos=%d len=%d pairs=%v", pos, len(body), obj.hash)
		}
	})

	t.Run("zset", func(t *testing.T) {
		body := appendRDBLen(nil, 3)
		body = appendRDBRawString(body, []byte("one"))
		body = append(body, byte(len("1.5")))
		body = append(body, []byte("1.5")...)
		body = appendRDBRawString(body, []byte("two"))
		body = append(body, byte(len("-2.25")))
		body = append(body, []byte("-2.25")...)
		body = appendRDBRawString(body, []byte("inf"))
		body = append(body, 254)

		pos := 0
		obj, err := decodeRedisRDBObjectAt(body, &pos, redisRDBTypeZSet)
		if err != nil {
			t.Fatal(err)
		}
		if pos != len(body) || len(obj.zset) != 3 ||
			string(obj.zset[0].Member) != "one" || obj.zset[0].Score != 1.5 ||
			string(obj.zset[1].Member) != "two" || obj.zset[1].Score != -2.25 ||
			string(obj.zset[2].Member) != "inf" || !math.IsInf(obj.zset[2].Score, 1) {
			t.Fatalf("unexpected legacy zset decode: pos=%d len=%d items=%v", pos, len(body), obj.zset)
		}
	})

	t.Run("zset rejects nan", func(t *testing.T) {
		body := appendRDBLen(nil, 1)
		body = appendRDBRawString(body, []byte("nan"))
		body = append(body, 253)
		pos := 0
		if _, err := decodeRedisRDBObjectAt(body, &pos, redisRDBTypeZSet); err == nil {
			t.Fatal("expected NaN legacy zset score rejection")
		}
	})

	t.Run("zset2", func(t *testing.T) {
		body := appendRDBLen(nil, 2)
		body = appendRDBRawString(body, []byte("one"))
		var score [8]byte
		binary.LittleEndian.PutUint64(score[:], math.Float64bits(1.5))
		body = append(body, score[:]...)
		body = appendRDBRawString(body, []byte("two"))
		binary.LittleEndian.PutUint64(score[:], math.Float64bits(-2.25))
		body = append(body, score[:]...)

		pos := 0
		obj, err := decodeRedisRDBObjectAt(body, &pos, redisRDBTypeZSet2)
		if err != nil {
			t.Fatal(err)
		}
		if pos != len(body) || len(obj.zset) != 2 ||
			string(obj.zset[0].Member) != "one" || obj.zset[0].Score != 1.5 ||
			string(obj.zset[1].Member) != "two" || obj.zset[1].Score != -2.25 {
			t.Fatalf("unexpected zset decode: pos=%d len=%d items=%v", pos, len(body), obj.zset)
		}
	})
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

func TestRedisPartialResyncKeepsRedisStreamMode(t *testing.T) {
	s := New(engine.New())
	s.replication.setReplica("127.0.0.1", 6379)
	s.replication.mu.Lock()
	s.replication.masterRunID = "0123456789012345678901234567890123456789"
	s.replication.offset = 100
	s.replication.masterRedisStream = true
	s.replication.mu.Unlock()

	client, upstream := net.Pipe()
	defer client.Close()
	defer upstream.Close()

	upstreamDone := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(upstream)

		args, _, err := readRedisReplicationCommand(reader)
		if err != nil {
			upstreamDone <- err
			return
		}
		if len(args) != 3 || string(args[0]) != "REPLCONF" || string(args[1]) != "capa" || string(args[2]) != "eof" {
			upstreamDone <- fmt.Errorf("unexpected REPLCONF: %q", args)
			return
		}
		if _, err := upstream.Write([]byte("+OK\r\n")); err != nil {
			upstreamDone <- err
			return
		}

		args, _, err = readRedisReplicationCommand(reader)
		if err != nil {
			upstreamDone <- err
			return
		}
		if len(args) != 3 || string(args[0]) != "PSYNC" ||
			string(args[1]) != "0123456789012345678901234567890123456789" ||
			string(args[2]) != "101" {
			upstreamDone <- fmt.Errorf("unexpected PSYNC: %q", args)
			return
		}

		wire := []byte("+CONTINUE\r\n")
		wire = append(wire, []byte("*3\r\n$3\r\nSET\r\n$11\r\npartial:key\r\n$2\r\nok\r\n")...)
		if _, err := upstream.Write(wire); err != nil {
			upstreamDone <- err
			return
		}

		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			value, found, wrong := s.store.GetString("partial:key")
			if found && !wrong && string(value) == "ok" {
				upstreamDone <- nil
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		upstreamDone <- errors.New("partial-resync Redis command was not applied")
	}()

	cancel := make(chan struct{})
	consumeDone := make(chan error, 1)
	go func() {
		consumeDone <- s.consumeReplicationConnection(client, cancel)
	}()

	if err := <-upstreamDone; err != nil {
		close(cancel)
		t.Fatal(err)
	}

	close(cancel)
	_ = client.Close()
	select {
	case <-consumeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("replication consumer did not stop")
	}
}

type oneByteReader struct {
	data []byte
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}

func TestRedisPartialResyncRecoversRedisStreamMode(t *testing.T) {
	s := New(engine.New())
	s.replication.setReplica("127.0.0.1", 6379)
	s.replication.mu.Lock()
	s.replication.masterRunID = "0123456789012345678901234567890123456789"
	s.replication.offset = 100
	s.replication.masterRedisStream = false
	s.replication.mu.Unlock()

	client, upstream := net.Pipe()
	defer client.Close()
	defer upstream.Close()

	upstreamDone := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(upstream)

		args, _, err := readRedisReplicationCommand(reader)
		if err != nil {
			upstreamDone <- err
			return
		}
		if len(args) != 3 || string(args[0]) != "REPLCONF" {
			upstreamDone <- fmt.Errorf("unexpected REPLCONF: %q", args)
			return
		}
		if _, err := upstream.Write([]byte("+OK\r\n")); err != nil {
			upstreamDone <- err
			return
		}

		args, _, err = readRedisReplicationCommand(reader)
		if err != nil {
			upstreamDone <- err
			return
		}
		if len(args) != 3 || string(args[0]) != "PSYNC" {
			upstreamDone <- fmt.Errorf("unexpected PSYNC: %q", args)
			return
		}

		wire := []byte("+CONTINUE\r\n*3\r\n$3\r\nSET\r\n$11\r\npartial:key\r\n$2\r\nok\r\n")
		if _, err := upstream.Write(wire); err != nil {
			upstreamDone <- err
			return
		}

		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			value, found, wrong := s.store.GetString("partial:key")
			if found && !wrong && string(value) == "ok" {
				s.replication.mu.RLock()
				mode := s.replication.masterRedisStream
				s.replication.mu.RUnlock()
				if !mode {
					upstreamDone <- errors.New("Redis stream mode was not recovered")
					return
				}
				upstreamDone <- nil
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		upstreamDone <- errors.New("partial-resync Redis command was not applied")
	}()

	cancel := make(chan struct{})
	consumeDone := make(chan error, 1)
	go func() {
		consumeDone <- s.consumeReplicationConnection(client, cancel)
	}()

	if err := <-upstreamDone; err != nil {
		close(cancel)
		t.Fatal(err)
	}

	close(cancel)
	_ = client.Close()
	select {
	case <-consumeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("replication consumer did not stop")
	}
}

func TestReadRedisReplicationCommandHandlesFragmentedCRLF(t *testing.T) {
	wire := []byte("*1\r\n$4\r\nPING\r\n")
	reader := bufio.NewReaderSize(&oneByteReader{data: wire}, 1)

	args, n, err := readRedisReplicationCommand(reader)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(wire)) {
		t.Fatalf("bytes=%d want=%d", n, len(wire))
	}
	if len(args) != 1 || string(args[0]) != "PING" {
		t.Fatalf("args=%q", args)
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

func TestReplicationBacklogBoundaryOffsets(t *testing.T) {
	s := New(engine.New())
	r := &s.replication

	r.mu.Lock()
	r.runID = "0123456789012345678901234567890123456789"
	r.backlogSize = 12
	r.backlogActive = true
	r.offset = 0
	r.backlogFirstOffset = 1

	for _, frame := range [][]byte{
		[]byte("aaaa"),
		[]byte("bbbb"),
		[]byte("cccc"),
		[]byte("dddd"),
	} {
		r.appendBacklogLocked(frame, append([]byte("$x\r\n"), frame...))
	}

	first := r.backlogFirstOffset
	lastPlusOne := r.offset + 1
	runID := r.runID
	r.mu.Unlock()

	if first <= 1 {
		t.Fatalf("expected backlog eviction, first=%d", first)
	}

	r.mu.RLock()
	payloads, ok := r.partialSyncPayloadLocked(runID, first)
	r.mu.RUnlock()
	if !ok || len(payloads) == 0 {
		t.Fatalf("exact first backlog offset rejected: first=%d ok=%v payloads=%d", first, ok, len(payloads))
	}

	r.mu.RLock()
	_, ok = r.partialSyncPayloadLocked(runID, first-1)
	r.mu.RUnlock()
	if ok {
		t.Fatalf("expired backlog offset %d unexpectedly accepted", first-1)
	}

	r.mu.RLock()
	payloads, ok = r.partialSyncPayloadLocked(runID, lastPlusOne)
	r.mu.RUnlock()
	if !ok {
		t.Fatalf("master offset + 1 rejected: %d", lastPlusOne)
	}
	if len(payloads) != 0 {
		t.Fatalf("master offset + 1 returned %d payloads", len(payloads))
	}

	r.mu.RLock()
	_, ok = r.partialSyncPayloadLocked(runID, lastPlusOne+1)
	r.mu.RUnlock()
	if ok {
		t.Fatalf("offset beyond master+1 unexpectedly accepted")
	}
}

func TestReplicationPartialResyncReplaysFromContainingBacklogEntry(t *testing.T) {
	s := New(engine.New())
	r := &s.replication

	r.mu.Lock()
	r.runID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	r.backlogSize = 1024
	r.backlogActive = true
	r.offset = 0
	r.backlogFirstOffset = 1

	firstFrame := []byte("123456")
	secondFrame := []byte("abcdef")
	firstPayload := []byte("payload-one")
	secondPayload := []byte("payload-two")

	r.appendBacklogLocked(firstFrame, firstPayload)
	firstStart := r.backlog[0].startOffset
	firstEnd := r.backlog[0].endOffset
	r.appendBacklogLocked(secondFrame, secondPayload)
	runID := r.runID
	r.mu.Unlock()

	insideFirst := firstStart + 2
	if insideFirst > firstEnd {
		t.Fatalf("bad test setup: inside=%d end=%d", insideFirst, firstEnd)
	}

	r.mu.RLock()
	payloads, ok := r.partialSyncPayloadLocked(runID, insideFirst)
	r.mu.RUnlock()
	if !ok {
		t.Fatal("partial sync from inside retained entry rejected")
	}
	if len(payloads) != 2 {
		t.Fatalf("payload count=%d want=2", len(payloads))
	}
	if !bytes.Equal(payloads[0], firstPayload) || !bytes.Equal(payloads[1], secondPayload) {
		t.Fatalf("unexpected replay payloads: %q", payloads)
	}
}

func TestReplicationBacklogFirstOffsetTracksEvictionExactly(t *testing.T) {
	s := New(engine.New())
	r := &s.replication

	r.mu.Lock()
	r.backlogSize = 10
	r.backlogActive = true
	r.offset = 0
	r.backlogFirstOffset = 1

	r.appendBacklogLocked([]byte("123456"), []byte("one"))
	firstEnd := r.backlog[0].endOffset

	r.appendBacklogLocked([]byte("abcdef"), []byte("two"))

	if len(r.backlog) != 1 {
		got := len(r.backlog)
		r.mu.Unlock()
		t.Fatalf("backlog entries=%d want=1", got)
	}
	wantFirst := firstEnd + 1
	gotFirst := r.backlogFirstOffset
	gotBytes := r.backlogBytes
	gotOffset := r.offset
	r.mu.Unlock()

	if gotFirst != wantFirst {
		t.Fatalf("backlogFirstOffset=%d want=%d", gotFirst, wantFirst)
	}
	if gotBytes != 6 {
		t.Fatalf("backlogBytes=%d want=6", gotBytes)
	}
	if gotOffset != 12 {
		t.Fatalf("master offset=%d want=12", gotOffset)
	}
}

func TestInterruptedFullResyncDoesNotAdvanceContinuationState(t *testing.T) {
	s := New(engine.New())
	s.replication.init()
	s.replication.mu.Lock()
	s.replication.masterRunID = "oldoldoldoldoldoldoldoldoldoldoldoldoldold"
	s.replication.offset = 10
	s.replication.masterRedisStream = false
	s.replication.mu.Unlock()

	client, server := net.Pipe()
	defer client.Close()

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		defer server.Close()

		buf := make([]byte, 1024)
		_ = server.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := server.Read(buf); err != nil {
			return
		}
		if _, err := server.Write([]byte("+OK\r\n")); err != nil {
			return
		}

		_ = server.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := server.Read(buf); err != nil {
			return
		}

		const newRunID = "newnewnewnewnewnewnewnewnewnewnewnewnewnew"
		if _, err := server.Write([]byte("+FULLRESYNC " + newRunID + " 50\r\n$100\r\npartial")); err != nil {
			return
		}
	}()

	err := s.consumeReplicationConnection(client, make(chan struct{}))
	if err == nil {
		t.Fatal("expected interrupted FULLRESYNC error")
	}
	<-serverDone

	s.replication.mu.RLock()
	runID := s.replication.masterRunID
	offset := s.replication.offset
	redisStream := s.replication.masterRedisStream
	s.replication.mu.RUnlock()

	if runID != "oldoldoldoldoldoldoldoldoldoldoldoldoldold" {
		t.Fatalf("masterRunID advanced after interrupted FULLRESYNC: %q", runID)
	}
	if offset != 10 {
		t.Fatalf("offset advanced after interrupted FULLRESYNC: %d", offset)
	}
	if redisStream {
		t.Fatal("stream mode changed after interrupted FULLRESYNC")
	}
}

func TestInterruptedPartialResyncDoesNotAdvancePastIncompleteSnugFrame(t *testing.T) {
	s := New(engine.New())
	s.replication.init()
	s.replication.mu.Lock()
	s.replication.masterRunID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	s.replication.offset = 20
	s.replication.masterRedisStream = false
	s.replication.mu.Unlock()

	records := []persistence.Record{{Key: []byte("partial:ok"), Value: []byte("one")}}
	frame, err := encodeReplicationFrame(records)
	if err != nil {
		t.Fatal(err)
	}
	payload := replicationBulk(frame)

	client, server := net.Pipe()
	defer client.Close()

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		defer server.Close()

		buf := make([]byte, 1024)
		_ = server.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := server.Read(buf); err != nil {
			return
		}
		if _, err := server.Write([]byte("+OK\r\n")); err != nil {
			return
		}

		_ = server.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := server.Read(buf); err != nil {
			return
		}
		if _, err := server.Write([]byte("+CONTINUE\r\n")); err != nil {
			return
		}

		if _, err := server.Write(payload); err != nil {
			return
		}

		// Begin a second bulk frame, then disconnect before its body completes.
		_, _ = server.Write([]byte("$100\r\npartial"))
	}()

	err = s.consumeReplicationConnection(client, make(chan struct{}))
	if err == nil {
		t.Fatal("expected interrupted partial resync error")
	}
	<-serverDone

	v, found, wrong := s.store.GetString("partial:ok")
	if !found || wrong || string(v) != "one" {
		t.Fatalf("complete replay frame was not applied: found=%v wrong=%v value=%q", found, wrong, v)
	}

	s.replication.mu.RLock()
	offset := s.replication.offset
	runID := s.replication.masterRunID
	s.replication.mu.RUnlock()

	wantOffset := int64(20 + len(frame))
	if offset != wantOffset {
		t.Fatalf("offset=%d want=%d after interrupted trailing frame", offset, wantOffset)
	}
	if runID != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("runid changed during partial replay: %q", runID)
	}
}

func TestInterruptedPartialResyncDoesNotAdvancePastIncompleteRedisCommand(t *testing.T) {
	s := New(engine.New())
	s.replication.init()
	s.replication.mu.Lock()
	s.replication.masterRunID = "cccccccccccccccccccccccccccccccccccccccc"
	s.replication.offset = 40
	s.replication.masterRedisStream = true
	s.replication.mu.Unlock()

	complete := []byte("*3\r\n$3\r\nSET\r\n$11\r\npartial:cmd\r\n$3\r\none\r\n")

	client, server := net.Pipe()
	defer client.Close()

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		defer server.Close()

		buf := make([]byte, 1024)
		_ = server.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := server.Read(buf); err != nil {
			return
		}
		if _, err := server.Write([]byte("+OK\r\n")); err != nil {
			return
		}

		_ = server.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := server.Read(buf); err != nil {
			return
		}
		if _, err := server.Write([]byte("+CONTINUE\r\n")); err != nil {
			return
		}

		if _, err := server.Write(complete); err != nil {
			return
		}

		// Start a second Redis command and drop the connection mid-bulk.
		_, _ = server.Write([]byte("*3\r\n$3\r\nSET\r\n$12\r\npartial:bad\r\n$10\r\nabc"))
	}()

	err := s.consumeReplicationConnection(client, make(chan struct{}))
	if err == nil {
		t.Fatal("expected interrupted Redis partial resync error")
	}
	<-serverDone

	v, found, wrong := s.store.GetString("partial:cmd")
	if !found || wrong || string(v) != "one" {
		t.Fatalf("complete Redis replay command was not applied: found=%v wrong=%v value=%q", found, wrong, v)
	}
	if _, found, _ := s.store.GetString("partial:bad"); found {
		t.Fatal("incomplete Redis replay command mutated store")
	}

	s.replication.mu.RLock()
	offset := s.replication.offset
	runID := s.replication.masterRunID
	s.replication.mu.RUnlock()

	wantOffset := int64(40 + len(complete))
	if offset != wantOffset {
		t.Fatalf("offset=%d want=%d after interrupted Redis command", offset, wantOffset)
	}
	if runID != "cccccccccccccccccccccccccccccccccccccccc" {
		t.Fatalf("runid changed during Redis partial replay: %q", runID)
	}
}

func TestChainedReplicationPrimaryReplicaReplica(t *testing.T) {
	primary, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()

	middle, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer middle.Close()

	leaf, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer leaf.Close()

	primaryAddr := primary.listener.Addr().(*net.TCPAddr)
	if _, err := middle.server.Execute([][]byte{
		[]byte("REPLICAOF"), []byte("127.0.0.1"), []byte(strconv.Itoa(primaryAddr.Port)),
	}); err != nil {
		t.Fatal(err)
	}

	waitReplication(t, func() bool {
		return middle.server.replication.snapshot().masterLinkStatus == "up"
	})

	middleAddr := middle.listener.Addr().(*net.TCPAddr)
	if _, err := leaf.server.Execute([][]byte{
		[]byte("REPLICAOF"), []byte("127.0.0.1"), []byte(strconv.Itoa(middleAddr.Port)),
	}); err != nil {
		t.Fatal(err)
	}

	waitReplication(t, func() bool {
		return leaf.server.replication.snapshot().masterLinkStatus == "up"
	})
	waitReplication(t, func() bool {
		return middle.server.replication.snapshot().connectedReplicas == 1
	})

	if _, err := primary.server.Execute([][]byte{
		[]byte("SET"), []byte("chain:key"), []byte("value"),
	}); err != nil {
		t.Fatal(err)
	}

	waitReplication(t, func() bool {
		v, found, wrong := middle.server.store.GetString("chain:key")
		return found && !wrong && string(v) == "value"
	})
	waitReplication(t, func() bool {
		v, found, wrong := leaf.server.store.GetString("chain:key")
		return found && !wrong && string(v) == "value"
	})

	if state := primary.server.replication.snapshot(); state.connectedReplicas != 1 {
		t.Fatalf("primary connected replicas=%d want=1", state.connectedReplicas)
	}
	if state := middle.server.replication.snapshot(); state.connectedReplicas != 1 {
		t.Fatalf("middle connected replicas=%d want=1", state.connectedReplicas)
	}
}

func TestChainedReplicationLeafReconnectsThroughMiddle(t *testing.T) {
	primary, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()

	middle, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer middle.Close()

	leaf, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer leaf.Close()

	primaryAddr := primary.listener.Addr().(*net.TCPAddr)
	middleAddr := middle.listener.Addr().(*net.TCPAddr)

	if _, err := middle.server.Execute([][]byte{
		[]byte("REPLICAOF"), []byte("127.0.0.1"), []byte(strconv.Itoa(primaryAddr.Port)),
	}); err != nil {
		t.Fatal(err)
	}
	waitReplication(t, func() bool {
		return middle.server.replication.snapshot().masterLinkStatus == "up"
	})

	if _, err := leaf.server.Execute([][]byte{
		[]byte("REPLICAOF"), []byte("127.0.0.1"), []byte(strconv.Itoa(middleAddr.Port)),
	}); err != nil {
		t.Fatal(err)
	}
	waitReplication(t, func() bool {
		return leaf.server.replication.snapshot().masterLinkStatus == "up"
	})

	if _, err := primary.server.Execute([][]byte{
		[]byte("SET"), []byte("chain:before"), []byte("one"),
	}); err != nil {
		t.Fatal(err)
	}
	waitReplication(t, func() bool {
		v, found, wrong := leaf.server.store.GetString("chain:before")
		return found && !wrong && string(v) == "one"
	})

	leaf.server.stopReplicaFollow()
	waitReplication(t, func() bool {
		return middle.server.replication.snapshot().connectedReplicas == 0
	})

	if _, err := primary.server.Execute([][]byte{
		[]byte("SET"), []byte("chain:during"), []byte("two"),
	}); err != nil {
		t.Fatal(err)
	}
	waitReplication(t, func() bool {
		v, found, wrong := middle.server.store.GetString("chain:during")
		return found && !wrong && string(v) == "two"
	})

	leaf.server.startReplicaFollow("127.0.0.1", middleAddr.Port)
	waitReplication(t, func() bool {
		return leaf.server.replication.snapshot().masterLinkStatus == "up"
	})
	waitReplication(t, func() bool {
		v, found, wrong := leaf.server.store.GetString("chain:during")
		return found && !wrong && string(v) == "two"
	})
}

func TestChainedReplicationWaitCountsOnlyDirectReplicas(t *testing.T) {
	primary, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()

	middle, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer middle.Close()

	leaf, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer leaf.Close()

	primaryAddr := primary.listener.Addr().(*net.TCPAddr)
	middleAddr := middle.listener.Addr().(*net.TCPAddr)

	if _, err := middle.server.Execute([][]byte{
		[]byte("REPLICAOF"), []byte("127.0.0.1"), []byte(strconv.Itoa(primaryAddr.Port)),
	}); err != nil {
		t.Fatal(err)
	}
	waitReplication(t, func() bool {
		return middle.server.replication.snapshot().masterLinkStatus == "up"
	})

	if _, err := leaf.server.Execute([][]byte{
		[]byte("REPLICAOF"), []byte("127.0.0.1"), []byte(strconv.Itoa(middleAddr.Port)),
	}); err != nil {
		t.Fatal(err)
	}
	waitReplication(t, func() bool {
		return leaf.server.replication.snapshot().masterLinkStatus == "up"
	})
	waitReplication(t, func() bool {
		return primary.server.replication.snapshot().connectedReplicas == 1 &&
			middle.server.replication.snapshot().connectedReplicas == 1
	})

	if _, err := primary.server.Execute([][]byte{
		[]byte("SET"), []byte("chain:wait"), []byte("one"),
	}); err != nil {
		t.Fatal(err)
	}
	waitReplication(t, func() bool {
		v, found, wrong := leaf.server.store.GetString("chain:wait")
		return found && !wrong && string(v) == "one"
	})

	primary.server.replication.mu.RLock()
	primaryTarget := primary.server.replication.offset
	primary.server.replication.mu.RUnlock()

	got, err := primary.server.executeReplicationWait(
		[][]byte{[]byte("WAIT"), []byte("2"), []byte("100")},
		primaryTarget,
		nil,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != ":1\r\n" {
		t.Fatalf("primary WAIT counted transitive replica: %q", got)
	}

	middle.server.replication.mu.RLock()
	middleTarget := middle.server.replication.offset
	middle.server.replication.mu.RUnlock()

	got, err = middle.server.executeReplicationWait(
		[][]byte{[]byte("WAIT"), []byte("1"), []byte("1000")},
		middleTarget,
		nil,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != ":1\r\n" {
		t.Fatalf("middle WAIT did not count direct leaf: %q", got)
	}
}

func TestChainedReplicationWAITAOFCountsOnlyDirectFACKs(t *testing.T) {
	s := New(engine.New())

	middleID, _, _ := s.replication.registerReplica(func([]byte) error { return nil })
	defer s.replication.unregisterReplica(middleID)

	// Simulate the direct replica acknowledging both application and durable
	// persistence. A transitive leaf must not inflate the primary's count.
	s.replication.acknowledgeReplica(middleID, 200, 200)

	got, err := s.executeWaitAOF(
		[][]byte{[]byte("WAITAOF"), []byte("0"), []byte("2"), []byte("0")},
		200,
		0,
		nil,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "*2\r\n:0\r\n:1\r\n" {
		t.Fatalf("WAITAOF counted transitive replica: %q", got)
	}
}
