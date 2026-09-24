package server

import (
	"bufio"
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
