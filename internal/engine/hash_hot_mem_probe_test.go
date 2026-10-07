package engine

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

// Reports bytes per field for 1000-field hashes (64 B values) while hot and
// after maintenance has frozen them.
func TestHotHashBytesPerFieldReport(t *testing.T) {
	if testing.Short() {
		t.Skip("memory report")
	}
	s := New()
	clock := time.Now()
	s.now = func() time.Time { return clock }
	const items = 200_000
	v := bytes.Repeat([]byte("v"), 64)
	for i := 0; i < items; i++ {
		key := fmt.Sprintf("hash:%d", i/1000)
		if _, err := s.HashSetResults(key, [][]byte{[]byte(fmt.Sprintf("f:%06d", i%1000))}, [][]byte{v}); err != nil {
			t.Fatal(err)
		}
	}
	m := s.Memory()
	hotBytes := m.AccountedBytes
	if m.HotHashBytes == 0 {
		t.Fatal("expected HOT hashes after load")
	}
	t.Logf("hot:    accounted=%.1f hot=%.1f arena=%.1f B/field", float64(m.AccountedBytes)/items, float64(m.HotHashBytes)/items, float64(m.ArenaBytes)/items)
	clock = clock.Add(time.Minute)
	s.Compact(1 << 30)
	m = s.Memory()
	if m.HotHashBytes != 0 || m.AccountedBytes*10 > hotBytes*7 {
		t.Fatalf("idle compaction did not collapse HOT hashes: hot=%d accounted %d -> %d", m.HotHashBytes, hotBytes, m.AccountedBytes)
	}
	t.Logf("frozen: accounted=%.1f hot=%.1f arena=%.1f B/field", float64(m.AccountedBytes)/items, float64(m.HotHashBytes)/items, float64(m.ArenaBytes)/items)
}
