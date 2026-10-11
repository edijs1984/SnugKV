package engine

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func segmentFiles(t *testing.T, dir string) int {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, streamSegmentPrefix+"*"))
	return len(m)
}

func newSpillStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := NewWithOptions(Options{Shards: 4, StreamDir: dir, StreamRAMBytes: 128 << 10})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStreamSpillMatchesInMemoryStore(t *testing.T) {
	dir := t.TempDir()
	disk := newSpillStore(t, dir)
	mem, _ := NewWithShards(4)
	rng := rand.New(rand.NewSource(3))
	stores := []*Store{disk, mem}
	for _, s := range stores {
		s.StreamGroupCreate("q", "g", "0", true, -1)
	}
	for n := 1; n <= 12000; n++ {
		id := fmt.Sprintf("%d-0", n)
		f := testStreamFields(n, n)
		for _, s := range stores {
			if _, _, err := s.StreamAdd("q", id, f, StreamAddOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		if n%500 == 0 {
			ids := []StreamID{{Millis: uint64(rng.Intn(n) + 1)}, {Millis: uint64(rng.Intn(n) + 1)}}
			for _, s := range stores {
				s.StreamDelete("q", ids)
				s.StreamGroupRead([]string{"q"}, "g", "c", []StreamGroupReadCursor{{New: true}}, 100, false)
			}
		}
		if n == 9000 {
			for _, s := range stores {
				s.StreamTrimMaxLen("q", 6000, 0)
			}
		}
	}
	if segmentFiles(t, dir) == 0 {
		t.Fatal("nothing was spilled")
	}
	body := disk.streamLogs.get("q")
	if body.memory() > 600<<10 {
		t.Fatalf("resident bytes = %d", body.memory())
	}
	if body.dataBytes != mem.streamLogs.get("q").dataBytes || body.Len() != mem.streamLogs.get("q").Len() {
		t.Fatal("size differs from the in-memory store")
	}
	a, _ := disk.StreamRange("q", StreamRangeBound{}, StreamRangeBound{ID: StreamID{Millis: ^uint64(0), Sequence: ^uint64(0)}}, 0, false)
	b, _ := mem.StreamRange("q", StreamRangeBound{}, StreamRangeBound{ID: StreamID{Millis: ^uint64(0), Sequence: ^uint64(0)}}, 0, false)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("range differs")
	}
	if !reflect.DeepEqual(disk.streamLogs.get("q").packedEntries(nil), mem.streamLogs.get("q").packedEntries(nil)) {
		t.Fatal("entries differ")
	}
}

func TestStreamSpillFilesFollowTheMessages(t *testing.T) {
	dir := t.TempDir()
	s := newSpillStore(t, dir)
	for n := 1; n <= 10000; n++ {
		s.StreamAdd("q", fmt.Sprintf("%d-0", n), testStreamFields(n, n), StreamAddOptions{})
	}
	full := segmentFiles(t, dir)
	if full == 0 {
		t.Fatal("nothing spilled")
	}
	s.StreamTrimMaxLen("q", 2000, 0)
	if left := segmentFiles(t, dir); left >= full {
		t.Fatalf("trim did not release segments: %d -> %d", full, left)
	}
	// Deleting messages inside a spilled chunk pulls it back and stays correct.
	if n, _ := s.StreamDelete("q", []StreamID{{Millis: 8001}}); n != 1 {
		t.Fatalf("delete = %d", n)
	}
	if _, ok := s.streamLogs.get("q").Get(StreamID{Millis: 8001}); ok {
		t.Fatal("deleted entry still present")
	}
	s.Delete("q")
	if left := segmentFiles(t, dir); left != 0 {
		t.Fatalf("%d segments left after DEL", left)
	}
	if got := s.Memory().StreamBytes; got != 0 {
		t.Fatalf("stream bytes after DEL = %d", got)
	}
}

func TestStreamSpillLiftsSizeLimit(t *testing.T) {
	dir := t.TempDir()
	s := newSpillStore(t, dir)
	payload := make([]byte, 1000)
	for i := range payload {
		payload[i] = byte('a' + i%26)
	}
	f := []StreamField{{Field: []byte("p"), Value: payload}}
	for n := 1; n <= 36000; n++ { // about 36 MB, over the 32 MiB cap
		if _, _, err := s.StreamAdd("big", fmt.Sprintf("%d-0", n), f, StreamAddOptions{}); err != nil {
			t.Fatalf("add %d: %v", n, err)
		}
	}
	if l, _ := s.StreamLen("big"); l != 36000 {
		t.Fatalf("len = %d", l)
	}
	if res := s.streamLogs.get("big").memory(); res > 2<<20 {
		t.Fatalf("resident = %d", res)
	}
	old := streamExportInlineMax
	streamExportInlineMax = 1 << 20
	defer func() { streamExportInlineMax = old }()
	records := s.Export([]string{"big"})
	if len(records) < 5 || !records[0].Stream.Create || records[1].Stream.Create {
		t.Fatalf("export not split: %d records", len(records))
	}
	other := newSpillStore(t, t.TempDir())
	if err := other.Restore(records, false); err != nil {
		t.Fatal(err)
	}
	if l, _ := other.StreamLen("big"); l != 36000 {
		t.Fatalf("restored len = %d", l)
	}
	e, _ := other.streamLogs.get("big").Get(StreamID{Millis: 20000})
	if len(e.Fields) != 1 || string(e.Fields[0].Value) != string(payload) {
		t.Fatal("restored payload differs")
	}
}

func TestStreamSpillDirIsClearedAtStartup(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "seg-99.dat")
	os.WriteFile(stale, []byte("x"), 0o600)
	newSpillStore(t, dir)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale segment kept")
	}
}

func TestStreamSpillWithTransactionsAndRollback(t *testing.T) {
	dir := t.TempDir()
	s := newSpillStore(t, dir)
	rng := rand.New(rand.NewSource(5))
	keys := []string{"q1", "q2"}
	ops := randomStreamOps()
	for n := 1; n <= 4000; n++ {
		tx, ok := s.BeginStreamTx(keys)
		if !ok {
			t.Fatal("tx refused")
		}
		for k := 0; k < 1+rng.Intn(3); k++ {
			ops[rng.Intn(len(ops))](s, rng, n)
		}
		if n%4 == 0 {
			before := s.Export(keys)
			_ = before
		}
		if n%3 == 0 {
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		}
		tx.End()
	}
	// After all that the files must still be readable.
	for _, k := range keys {
		if _, err := s.StreamRange(k, StreamRangeBound{}, StreamRangeBound{ID: StreamID{Millis: ^uint64(0)}}, 0, false); err != nil {
			t.Fatal(err)
		}
	}
}
