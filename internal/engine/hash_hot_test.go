package engine

import (
	"fmt"
	"testing"
	"time"
)

func TestHotHashMutationReadLifecycle(t *testing.T) {
	s := New()
	key := "hot-lifecycle"

	fields := make([][]byte, hotHashPromoteFields)
	values := make([][]byte, hotHashPromoteFields)
	for i := range fields {
		fields[i] = []byte(fmt.Sprintf("field:%03d", i))
		values[i] = []byte(fmt.Sprintf("value:%03d", i))
	}
	if _, err := s.HashSetResults(key, fields, values); err != nil {
		t.Fatal(err)
	}

	sh := s.shardFor(key)
	sh.mu.RLock()
	e, ok := sh.get(key)
	isHot := ok && e.isHotHash()
	sh.mu.RUnlock()
	if !isHot {
		t.Fatal("large pipelined HASH did not promote to HOT representation")
	}

	// Repeated mutation of an already-HOT large HASH must remain HOT.
	if _, err := s.HashSetResults(
		key,
		[][]byte{[]byte("field:000"), []byte("field:new")},
		[][]byte{[]byte("updated"), []byte("new-value")},
	); err != nil {
		t.Fatal(err)
	}

	sh.mu.RLock()
	e, ok = sh.get(key)
	stillHot := ok && e.isHotHash()
	sh.mu.RUnlock()
	if !stillHot {
		t.Fatal("repeated mutation left HOT representation")
	}

	value, found, err := s.HashGet(key, []byte("field:000"))
	if err != nil || !found || string(value) != "updated" {
		t.Fatalf("field:000=%q found=%v err=%v", value, found, err)
	}

	value, found, err = s.HashGet(key, []byte("field:new"))
	if err != nil || !found || string(value) != "new-value" {
		t.Fatalf("field:new=%q found=%v err=%v", value, found, err)
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


func TestTinyPipelinedHashStaysCold(t *testing.T) {
	s := New()

	fields := make([][]byte, 10)
	values := make([][]byte, 10)
	for i := range fields {
		fields[i] = []byte{byte('a' + i)}
		values[i] = []byte("value")
	}
	if _, err := s.HashSetResults("tiny-pipeline", fields, values); err != nil {
		t.Fatal(err)
	}

	sh := s.shardFor("tiny-pipeline")
	sh.mu.RLock()
	e, ok := sh.get("tiny-pipeline")
	sh.mu.RUnlock()
	if !ok {
		t.Fatal("missing tiny pipeline hash")
	}
	if e.isHotHash() {
		t.Fatal("tiny pipeline hash should remain cold")
	}
}

func TestHotHashCompactionReturnsToIndexedAndAccountingStaysSane(t *testing.T) {
	s := New()

	fields := make([][]byte, hotHashPromoteFields)
	values := make([][]byte, hotHashPromoteFields)
	for i := range fields {
		fields[i] = []byte(fmt.Sprintf("field:%03d", i))
		values[i] = []byte("0123456789abcdef0123456789abcdef")
	}
	if _, err := s.HashSetResults("hot-indexed", fields, values); err != nil {
		t.Fatal(err)
	}

	sh := s.shardFor("hot-indexed")
	sh.mu.RLock()
	e, ok := sh.get("hot-indexed")
	isHot := ok && e.isHotHash()
	var h *hotHash
	if isHot {
		h, _, _ = sh.hotHashForKey("hot-indexed")
	}
	sh.mu.RUnlock()
	if !isHot || h == nil {
		t.Fatal("expected large pipelined hash to promote HOT")
	}

	before := s.Memory()
	if before.AccountedBytes == 0 || before.AccountedBytes > 1<<40 {
		t.Fatalf("invalid accounted memory before compact: %d", before.AccountedBytes)
	}

	// Active HOT hashes intentionally block generic compaction.
	if compacted := s.Compact(1 << 30); compacted != 0 {
		t.Fatalf("active HOT hash compacted = %d, want 0", compacted)
	}

	// Once the hash is truly idle, generic compaction may freeze it back to
	// indexed cold storage.
	sh.mu.Lock()
	h, _, ok = sh.hotHashForKey("hot-indexed")
	if !ok || h == nil {
		sh.mu.Unlock()
		t.Fatal("HOT hash disappeared before idle compaction")
	}
	h.lastMutation = s.now().Add(-hotHashIdleFreeze - time.Second).UnixMilli()
	sh.mu.Unlock()

	if compacted := s.Compact(1 << 30); compacted == 0 {
		t.Fatal("expected idle HOT hash compaction")
	}

	sh.mu.RLock()
	e, ok = sh.get("hot-indexed")
	if !ok {
		sh.mu.RUnlock()
		t.Fatal("hash missing after compaction")
	}
	isHot = e.isHotHash()
	var physical []byte
	if !isHot {
		physical = append([]byte(nil), sh.encoded(e)...)
	}
	sh.mu.RUnlock()

	if isHot {
		t.Fatal("idle compaction should freeze HOT hash")
	}
	if !isIndexedHash(physical) {
		t.Fatalf("idle compaction froze HOT hash to non-indexed encoding: %x", physical[:min(4, len(physical))])
	}

	after := s.Memory()
	if after.AccountedBytes == 0 || after.AccountedBytes > 1<<40 {
		t.Fatalf("invalid accounted memory after compact: %d", after.AccountedBytes)
	}
	if after.HotHashBytes != 0 {
		t.Fatalf("HOT hash bytes after compact = %d, want 0", after.HotHashBytes)
	}

	s.FlushDB()
	final := s.Memory()
	if final.AccountedBytes > 1<<40 {
		t.Fatalf("accounting wrapped after flush: %d", final.AccountedBytes)
	}
}

func TestHotHashSlotGrowthFlushReturnsToStructuralBaseline(t *testing.T) {
	s, err := NewWithShards(8)
	if err != nil {
		t.Fatal(err)
	}
	baseline := s.Memory().AccountedBytes

	fields := make([][]byte, hotHashPromoteFields)
	values := make([][]byte, hotHashPromoteFields)
	for i := range fields {
		fields[i] = []byte(fmt.Sprintf("f:%03d", i))
		values[i] = []byte("0123456789abcdef0123456789abcdef")
	}

	for i := 0; i < 200; i++ {
		key := fmt.Sprintf("hot-grow:%03d", i)
		if _, err := s.HashSetResults(key, fields, values); err != nil {
			t.Fatalf("key %d: %v", i, err)
		}
	}

	before := s.Memory()
	if before.HotHashBytes == 0 {
		t.Fatal("expected HOT hash accounting before flush")
	}

	s.FlushDB()

	after := s.Memory()
	if after.AccountedBytes != baseline {
		t.Fatalf("accounted after FLUSHDB = %d, want structural baseline %d", after.AccountedBytes, baseline)
	}
	if after.HotHashBytes != 0 {
		t.Fatalf("HOT hash bytes after FLUSHDB = %d, want 0", after.HotHashBytes)
	}
}


func TestMediumPipelinedHashStaysCold(t *testing.T) {
	s := New()

	fields := make([][]byte, 100)
	values := make([][]byte, 100)
	for i := range fields {
		fields[i] = []byte(fmt.Sprintf("field:%03d", i))
		values[i] = []byte("value")
	}
	if _, err := s.HashSetResults("medium-pipeline", fields, values); err != nil {
		t.Fatal(err)
	}

	sh := s.shardFor("medium-pipeline")
	sh.mu.RLock()
	e, ok := sh.get("medium-pipeline")
	sh.mu.RUnlock()
	if !ok {
		t.Fatal("missing medium pipeline hash")
	}
	if e.isHotHash() {
		t.Fatal("100-field hash should remain cold")
	}
}

func TestActiveLargeHotHashSurvivesGenericCompaction(t *testing.T) {
	s := New()

	fields := make([][]byte, hotHashPromoteFields)
	values := make([][]byte, hotHashPromoteFields)
	for i := range fields {
		fields[i] = []byte(fmt.Sprintf("field:%03d", i))
		values[i] = []byte("0123456789abcdef")
	}
	if _, err := s.HashSetResults("large-hot", fields, values); err != nil {
		t.Fatal(err)
	}

	sh := s.shardFor("large-hot")
	sh.mu.RLock()
	e, ok := sh.get("large-hot")
	isHot := ok && e.isHotHash()
	sh.mu.RUnlock()
	if !isHot {
		t.Fatal("large pipelined hash did not promote HOT")
	}

	_ = s.Compact(1 << 30)

	sh.mu.RLock()
	e, ok = sh.get("large-hot")
	stillHot := ok && e.isHotHash()
	sh.mu.RUnlock()
	if !stillHot {
		t.Fatal("active large HOT hash should not freeze during generic compaction")
	}
}


func TestIncrementalPipelinedHashPromotesOnlyAtLargeThreshold(t *testing.T) {
	s := New()
	key := "incremental-hot-policy"

	for i := 0; i < hotHashPromoteFields-1; i++ {
		field := []byte(fmt.Sprintf("field:%03d", i))
		if _, err := s.HashSetResults(key, [][]byte{field}, [][]byte{[]byte("value")}); err != nil {
			t.Fatalf("field %d: %v", i, err)
		}

		sh := s.shardFor(key)
		sh.mu.RLock()
		e, ok := sh.get(key)
		isHot := ok && e.isHotHash()
		sh.mu.RUnlock()
		if isHot {
			t.Fatalf("hash promoted HOT at %d fields, threshold is %d", i+1, hotHashPromoteFields)
		}
	}

	field := []byte(fmt.Sprintf("field:%03d", hotHashPromoteFields-1))
	if _, err := s.HashSetResults(key, [][]byte{field}, [][]byte{[]byte("value")}); err != nil {
		t.Fatal(err)
	}

	sh := s.shardFor(key)
	sh.mu.RLock()
	e, ok := sh.get(key)
	isHot := ok && e.isHotHash()
	sh.mu.RUnlock()
	if !isHot {
		t.Fatalf("hash did not promote HOT at %d fields", hotHashPromoteFields)
	}
}


func TestHotHashIdleFreezeWindow(t *testing.T) {
	s := New()
	now := time.Unix(1700000000, 0)
	s.now = func() time.Time { return now }

	h := &hotHash{lastMutation: now.UnixMilli()}
	if s.hotHashIdle(h) {
		t.Fatal("newly mutated HOT hash should remain active")
	}

	now = now.Add(hotHashIdleFreeze - time.Millisecond)
	if s.hotHashIdle(h) {
		t.Fatal("HOT hash froze before idle window elapsed")
	}

	now = now.Add(time.Millisecond)
	if !s.hotHashIdle(h) {
		t.Fatal("HOT hash did not become idle at freeze window")
	}
}
