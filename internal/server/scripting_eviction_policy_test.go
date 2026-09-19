package server

import (
	"errors"
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func newLuaEvictionTestServer(t *testing.T, policy string) *Server {
	t.Helper()

	store, err := engine.NewWithOptions(engine.Options{Shards: 1})
	if err != nil {
		t.Fatal(err)
	}
	s := New(store)
	s.eviction = policy
	return s
}

func setLuaEvictionHeadroom(t *testing.T, s *Server, headroom uint64) {
	t.Helper()
	used := s.store.Memory().AccountedBytes
	s.store.SetMaxMemory(used + headroom)
}

func TestLuaAllKeysLRUEvictsBeforeOOM(t *testing.T) {
	s := newLuaEvictionTestServer(t, "allkeys-lru")

	execute(t, s, "SET", "evict:a", strings.Repeat("a", 4096))
	execute(t, s, "SET", "evict:b", strings.Repeat("b", 4096))
	setLuaEvictionHeadroom(t, s, 512)

	reply, err := s.Execute(stringArgs(
		"EVAL",
		"#!lua\nreturn redis.call('SET','evict:new',ARGV[1])",
		"0",
		strings.Repeat("n", 8192),
	))
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != "+OK\r\n" {
		t.Fatalf("EVAL reply = %q", reply)
	}

	if value, ok := s.store.Get("evict:new"); !ok || len(value) != 8192 {
		t.Fatalf("evict:new missing or wrong size: ok=%v len=%d", ok, len(value))
	}

	_, a := s.store.Get("evict:a")
	_, b := s.store.Get("evict:b")
	if a && b {
		t.Fatal("allkeys-lru did not evict any existing victim")
	}
}

func TestLuaVolatileLRUEvictsTTLKeyAndPreservesPersistentKey(t *testing.T) {
	s := newLuaEvictionTestServer(t, "volatile-lru")

	execute(t, s, "SET", "evict:persistent", strings.Repeat("p", 4096))
	execute(t, s, "SET", "evict:volatile", strings.Repeat("v", 4096), "EX", "600")
	setLuaEvictionHeadroom(t, s, 512)

	reply, err := s.Execute(stringArgs(
		"EVAL",
		"#!lua flags=allow-oom\nreturn redis.call('SET','evict:new',ARGV[1])",
		"0",
		strings.Repeat("n", 8192),
	))
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != "+OK\r\n" {
		t.Fatalf("EVAL reply = %q", reply)
	}

	if _, ok := s.store.Get("evict:persistent"); !ok {
		t.Fatal("volatile-lru evicted persistent key")
	}
	if _, ok := s.store.Get("evict:volatile"); ok {
		t.Fatal("allow-oom bypassed normal volatile-lru eviction")
	}
	if value, ok := s.store.Get("evict:new"); !ok || len(value) != 8192 {
		t.Fatalf("evict:new missing or wrong size: ok=%v len=%d", ok, len(value))
	}
}

func TestLuaVolatileLRUNoEligibleVictimLegacyOOM(t *testing.T) {
	s := newLuaEvictionTestServer(t, "volatile-lru")

	execute(t, s, "SET", "evict:persistent", strings.Repeat("p", 4096))
	setLuaEvictionHeadroom(t, s, 512)

	_, err := s.Execute(stringArgs(
		"EVAL",
		"return redis.call('SET','evict:new',ARGV[1])",
		"0",
		strings.Repeat("n", 8192),
	))
	if err == nil || (!errors.Is(err, engine.ErrOOM) &&
		!strings.Contains(err.Error(), "OOM command not allowed")) {
		t.Fatalf("legacy EVAL error = %v, want OOM", err)
	}

	if _, ok := s.store.Get("evict:persistent"); !ok {
		t.Fatal("persistent key changed after OOM")
	}
	if _, ok := s.store.Get("evict:new"); ok {
		t.Fatal("failed legacy script created destination")
	}
}

func TestLuaVolatileLRUNoEligibleVictimAllowOOMSucceeds(t *testing.T) {
	s := newLuaEvictionTestServer(t, "volatile-lru")

	execute(t, s, "SET", "evict:persistent", strings.Repeat("p", 4096))
	setLuaEvictionHeadroom(t, s, 512)
	limit := s.store.MaxMemory()

	reply, err := s.Execute(stringArgs(
		"EVAL",
		"#!lua flags=allow-oom\nreturn redis.call('SET','evict:new',ARGV[1])",
		"0",
		strings.Repeat("n", 8192),
	))
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != "+OK\r\n" {
		t.Fatalf("EVAL reply = %q", reply)
	}

	if _, ok := s.store.Get("evict:persistent"); !ok {
		t.Fatal("allow-oom removed persistent key")
	}
	if value, ok := s.store.Get("evict:new"); !ok || len(value) != 8192 {
		t.Fatalf("evict:new missing or wrong size: ok=%v len=%d", ok, len(value))
	}
	if got := s.store.MaxMemory(); got != limit {
		t.Fatalf("maxmemory = %d after script, want %d", got, limit)
	}
}
