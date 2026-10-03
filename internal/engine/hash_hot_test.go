package engine

import (
	"testing"
	"time"
)

func TestHotHashMutationReadLifecycle(t *testing.T) {
	s := New()

	if added, err := s.HashSet("hot", [][]byte{[]byte("a")}, [][]byte{[]byte("1")}); err != nil || added != 1 {
		t.Fatalf("initial HSET added=%d err=%v", added, err)
	}

	sh := s.shardFor("hot")
	sh.mu.RLock()
	e, ok := sh.get("hot")
	firstHot := ok && e.isHotHash()
	sh.mu.RUnlock()
	if firstHot {
		t.Fatal("first HSET should remain cold")
	}

	if added, err := s.HashSet(
		"hot",
		[][]byte{[]byte("b"), []byte("a")},
		[][]byte{[]byte("2"), []byte("3")},
	); err != nil || added != 1 {
		t.Fatalf("second HSET added=%d err=%v", added, err)
	}

	sh.mu.RLock()
	e, ok = sh.get("hot")
	isHot := ok && e.isHotHash()
	h, _, hotOK := sh.hotHashForKey("hot")
	sh.mu.RUnlock()
	if !isHot || !hotOK || h == nil {
		t.Fatal("repeated mutation did not promote HASH to HOT representation")
	}

	value, found, err := s.HashGet("hot", []byte("a"))
	if err != nil || !found || string(value) != "3" {
		t.Fatalf("HGET a=%q found=%v err=%v", value, found, err)
	}
	if n, err := s.HashLen("hot"); err != nil || n != 2 {
		t.Fatalf("HLEN=%d err=%v", n, err)
	}
	if deleted, err := s.HashDel("hot", [][]byte{[]byte("b")}); err != nil || deleted != 1 {
		t.Fatalf("HDEL=%d err=%v", deleted, err)
	}

	stats := s.Memory()
	if stats.HotHashBytes == 0 {
		t.Fatal("HOT hash memory is not accounted")
	}
}

func TestHotHashExportRestoreLatestValue(t *testing.T) {
	s := New()
	if _, err := s.HashSet("persist-hot", [][]byte{[]byte("a")}, [][]byte{[]byte("1")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HashSet("persist-hot", [][]byte{[]byte("a"), []byte("b")}, [][]byte{[]byte("2"), []byte("3")}); err != nil {
		t.Fatal(err)
	}

	records := s.Export([]string{"persist-hot"})
	if len(records) != 1 {
		t.Fatalf("records=%d want=1", len(records))
	}

	restored := New()
	if err := restored.Restore(records, false); err != nil {
		t.Fatal(err)
	}
	value, found, err := restored.HashGet("persist-hot", []byte("a"))
	if err != nil || !found || string(value) != "2" {
		t.Fatalf("restored a=%q found=%v err=%v", value, found, err)
	}
	value, found, err = restored.HashGet("persist-hot", []byte("b"))
	if err != nil || !found || string(value) != "3" {
		t.Fatalf("restored b=%q found=%v err=%v", value, found, err)
	}
}

func TestHotHashFieldExpiryFreezesToCold(t *testing.T) {
	s := New()
	if _, err := s.HashSet("ttl-hot", [][]byte{[]byte("a")}, [][]byte{[]byte("1")}); err != nil {
		t.Fatal(err)
	}

	when := time.Now().Add(time.Minute).UnixMilli()
	result, err := s.HashFieldExpireAt("ttl-hot", [][]byte{[]byte("a")}, when)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0] != 1 {
		t.Fatalf("HEXPIRE result=%v", result)
	}

	sh := s.shardFor("ttl-hot")
	sh.mu.RLock()
	e, ok := sh.get("ttl-hot")
	sh.mu.RUnlock()
	if !ok || e.isHotHash() {
		t.Fatal("field-expiry HASH should be cold")
	}
}

func TestCompactFreezesHotHash(t *testing.T) {
	s := New()
	if _, err := s.HashSet("compact-hot", [][]byte{[]byte("a")}, [][]byte{[]byte("1")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HashSet("compact-hot", [][]byte{[]byte("b")}, [][]byte{[]byte("2")}); err != nil {
		t.Fatal(err)
	}

	if compacted := s.Compact(1 << 30); compacted == 0 {
		t.Fatal("expected at least one shard compaction")
	}

	sh := s.shardFor("compact-hot")
	sh.mu.RLock()
	e, ok := sh.get("compact-hot")
	sh.mu.RUnlock()
	if !ok || e.isHotHash() {
		t.Fatal("compaction should freeze HOT HASH")
	}

	value, found, err := s.HashGet("compact-hot", []byte("b"))
	if err != nil || !found || string(value) != "2" {
		t.Fatalf("post-compact b=%q found=%v err=%v", value, found, err)
	}
}
