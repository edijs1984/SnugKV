package engine

import (
	"fmt"
	"testing"
	"time"
)

func auditMemory(t *testing.T, s *Store) {
	t.Helper()
	unlock := s.lockAll()
	defer unlock()
	var entries uint64
	var metas uint64
	index := structuralMemoryBytes(len(s.shards))
	var arenaBytes uint64
	schemaBytes := uint64(0)
	if s.shards[0].shapes != nil {
		schemaBytes = uint64(len(s.shards) * (2*256 + 2048 + 1024))
	}
	for i := range s.shards {
		sh := &s.shards[i]

		index += sh.data.CapacityBytes()
		arenaBytes += sh.arena.TotalMemoryBytes()

		// EntryBytes includes the physical reserved []entry pool plus
		// live key bytes. Deleted/free entry slots remain allocated
		// until the shard is compacted/reset.
		entries += uint64(cap(sh.entries)) * entryStructBytes
		if sh.metas != nil {
			entries += uint64(cap(*sh.metas)) * entryMetaSlotBytes
		}

		for k, e := range sh.all() {
			entries += entryCharge(k, e)
			metas += metadataCharge(e)
		}
	}
	m := s.Memory()
	if m.AccountedBytes != index+entries+arenaBytes+schemaBytes+metas ||
		m.ArenaBytes != arenaBytes ||
		m.SchemaBytes != schemaBytes ||
		m.IndexReservedBytes != index ||
		m.EntryBytes != entries ||
		m.MetaBytes != metas {
		t.Fatalf("audit %+v want index=%d entries=%d metas=%d", m, index, entries, metas)
	}
}
func TestMemoryLimitAtomicity(t *testing.T) {
	s, err := NewWithOptions(Options{Shards: 1, MaxMemory: 100000, Encoding: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Set("k", []byte("123456789"), 10000); err != nil {
		t.Fatal(err)
	}
	before := s.Memory()
	big := make([]byte, 200000)
	if err = s.Set("k", big, 0); err != ErrOOM {
		t.Fatal(err)
	}
	if _, _, err = s.GetSet("k", big); err != ErrOOM {
		t.Fatal(err)
	}
	if err = s.MSet([]string{"k", "new"}, [][]byte{[]byte("changed"), big}); err != ErrOOM {
		t.Fatal(err)
	}
	if got, _ := s.Get("k"); string(got) != "123456789" || s.TTL("k", true) < 0 {
		t.Fatal("failed write mutated value or TTL")
	}
	if s.Memory() != before {
		t.Fatal("failed writes changed accounting")
	}
	auditMemory(t, s)
	s.Delete("k")
	auditMemory(t, s)
	now := time.Unix(100, 0)
	s.now = func() time.Time { return now }
	s.Set("k", []byte("value"), 1)
	now = now.Add(time.Millisecond)
	s.CleanupExpired()
	auditMemory(t, s)
}
func TestEncodedEngineRoundTrip(t *testing.T) {
	s, _ := NewWithOptions(Options{Shards: 4, Encoding: true})
	for _, v := range []string{"123456789", "0042", "550e8400-e29b-41d4-a716-446655440000", "2026-09-07T12:34:56Z", "\x00\xff"} {
		s.Set("k", []byte(v), 0)
		got, ok := s.Get("k")
		if !ok || string(got) != v {
			t.Fatal("roundtrip")
		}
		auditMemory(t, s)
	}
	s.Set("counter", []byte("123456789"), 0)
	s.Incr("counter")
	if got, _ := s.Get("counter"); string(got) != "123456790" {
		t.Fatal("encoded increment")
	}
	auditMemory(t, s)
}

func TestExpirationHeapChurn(t *testing.T) {
	s := New()
	now := time.Unix(100, 0)
	s.now = func() time.Time { return now }
	for i := 0; i < 1000; i++ {
		s.Set("k", []byte("v"), 100)
		s.Expire("k", time.Second)
	}
	sh := s.shardFor("k")
	if sh.expiration.Len() != 1 {
		t.Fatal("TTL heap grew under updates")
	}
	s.Persist("k")
	if sh.expiration.Len() != 0 {
		t.Fatal("persist retained heap item")
	}
	s.Expire("k", time.Second)
	s.Set("k", []byte("new"), 0)
	now = now.Add(time.Second)
	if s.CleanupExpiredLimit(1) != 0 {
		t.Fatal("stale expiration removed overwrite")
	}
	auditMemory(t, s)
}

func TestArenaBatchAccountingChurn(t *testing.T) {
	s, _ := NewWithOptions(Options{Shards: 1})
	keys := []string{"a", "b", "c", "d"}
	for i := 0; i < 20; i++ {
		values := [][]byte{make([]byte, 100+i), make([]byte, 40000), make([]byte, 17000), make([]byte, 1000)}
		if err := s.MSet(keys, values); err != nil {
			t.Fatal(err)
		}
		auditMemory(t, s)
		got, found := s.MGet(keys)
		for j := range keys {
			if !found[j] || len(got[j]) != len(values[j]) {
				t.Fatal("batch value lost")
			}
		}
		s.DeleteMany(keys)
		auditMemory(t, s)
	}

}

func TestCompactionPreservesLiveBytesAndTTL(t *testing.T) {
	s, _ := NewWithOptions(Options{Shards: 1})
	now := time.Unix(100, 0)
	s.now = func() time.Time { return now }
	for i := 0; i < 20; i++ {
		s.Set(fmt.Sprint(i), make([]byte, 10000), 60000)
	}
	for i := 0; i < 19; i++ {
		s.Delete(fmt.Sprint(i))
	}
	before := s.Memory()
	if s.Compact(16<<20) != 1 {
		t.Fatal("compaction skipped")
	}
	after := s.Memory()
	if after.AccountedBytes >= before.AccountedBytes {
		t.Fatal("no reclaimed capacity")
	}
	got, ok := s.Get("19")
	if !ok || len(got) != 10000 || s.TTL("19", true) != 60000 {
		t.Fatal("compaction changed live record")
	}
	auditMemory(t, s)
	s.Delete("19")
	s.Compact(16 << 20)
	auditMemory(t, s)
}

func TestMemoryUsage(t *testing.T) {
	s := New()

	if err := s.Set("hello", []byte("world"), 0); err != nil {
		t.Fatal(err)
	}

	usage, found := s.MemoryUsage("hello")

	if !found {
		t.Fatal("expected key")
	}

	if usage == 0 {
		t.Fatal("expected non-zero memory usage")
	}

	t.Logf("MEMORY USAGE hello = %d bytes", usage)
}


func TestEncodedCounterUsesInlineStorage(t *testing.T) {
	s, err := NewWithOptions(Options{Shards: 1, Encoding: true})
	if err != nil {
		t.Fatal(err)
	}

	const key = "counter"
	const value = "1000000000"
	if err := s.Set(key, []byte(value), 0); err != nil {
		t.Fatal(err)
	}

	sh := s.shardFor(key)
	sh.mu.RLock()
	entry, ok := sh.get(key)
	arenaBytes := sh.arena.TotalMemoryBytes()
	sh.mu.RUnlock()
	if !ok {
		t.Fatal("counter missing")
	}
	if !entry.ref.IsInline() {
		t.Fatal("encoded counter was not stored inline")
	}
	if arenaBytes != 0 {
		t.Fatalf("inline counter reserved %d arena bytes, want 0", arenaBytes)
	}

	name, logical, encoded, ok := s.Encoding(key)
	if !ok || name != "integer" || logical != len(value) || encoded >= logical {
		t.Fatalf("encoding=%s logical=%d encoded=%d ok=%v", name, logical, encoded, ok)
	}

	got, ok := s.Get(key)
	if !ok || string(got) != value {
		t.Fatalf("round trip=%q ok=%v", got, ok)
	}

	if _, err := s.Incr(key); err != nil {
		t.Fatal(err)
	}
	got, ok = s.Get(key)
	if !ok || string(got) != "1000000001" {
		t.Fatalf("incremented round trip=%q ok=%v", got, ok)
	}

	sh.mu.RLock()
	entry, _ = sh.get(key)
	arenaBytes = sh.arena.TotalMemoryBytes()
	metaSlots := 0
	if sh.metas != nil {
		metaSlots = cap(*sh.metas)
	}
	sh.mu.RUnlock()
	if !entry.ref.IsInline() || arenaBytes != 0 {
		t.Fatalf("increment lost inline storage: inline=%v arena=%d", entry.ref.IsInline(), arenaBytes)
	}
	if metaSlots != 0 {
		t.Fatalf("metadata-free counter allocated %d metadata slots", metaSlots)
	}
	if entryStructBytes != 24 {
		t.Fatalf("stored entry size=%d want=24", entryStructBytes)
	}

	auditMemory(t, s)
}
