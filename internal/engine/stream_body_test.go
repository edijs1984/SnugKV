package engine

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"snugkv/internal/persistence"
)

func testStreamFields(n int, seed int) []StreamField {
	out := make([]StreamField, 0, 1+n%3)
	for i := 0; i <= n%3; i++ {
		out = append(out, StreamField{
			Field: []byte(fmt.Sprintf("f%d", i)),
			Value: []byte(fmt.Sprintf("value-%d-%d-%0*d", seed, i, n%40, 0)),
		})
	}
	return out
}

func checkBodyInvariants(t *testing.T, b *streamBody) {
	t.Helper()
	var length int
	var dataBytes, dataCap uint64
	var prev StreamID
	for ci := range b.chunks {
		c := &b.chunks[ci]
		if c.count == 0 {
			t.Fatalf("chunk %d is empty", ci)
		}
		off := c.head
		for k := 0; k < c.count; k++ {
			id := streamEntryIDAt(c.data, off)
			if k == 0 && !id.equal(c.first) {
				t.Fatalf("chunk %d first = %s, want %s", ci, c.first, id)
			}
			if length > 0 && !prev.less(id) {
				t.Fatalf("ids out of order: %s then %s", prev, id)
			}
			if k == c.count-1 {
				if !id.equal(c.last) || off != c.lastOff {
					t.Fatalf("chunk %d last/lastOff = %s/%d, want %s/%d", ci, c.last, c.lastOff, id, off)
				}
			}
			prev = id
			length++
			off = streamEntryEnd(c.data, off)
		}
		if off != len(c.data) {
			t.Fatalf("chunk %d has %d trailing bytes", ci, len(c.data)-off)
		}
		dataBytes += uint64(len(c.data) - c.head)
		dataCap += uint64(cap(c.data))
	}
	if length != b.length || dataBytes != b.dataBytes || dataCap != b.dataCap {
		t.Fatalf("counters length/bytes/cap = %d/%d/%d, want %d/%d/%d", b.length, b.dataBytes, b.dataCap, length, dataBytes, dataCap)
	}
}

func TestStreamBodyMatchesModel(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	b := newStreamBody()
	var model []StreamEntry
	next := uint64(1)
	add := func() {
		id := StreamID{Millis: next, Sequence: uint64(rng.Intn(3))}
		next++
		fields := testStreamFields(rng.Intn(400), int(next))
		b.Append(id, fields)
		model = append(model, StreamEntry{ID: id, Fields: fields})
	}
	maxChunks := 0
	for round := 0; round < 400; round++ {
		for i, n := 0, 1+rng.Intn(200); i < n; i++ {
			add()
		}
		switch rng.Intn(4) {
		case 0:
			n := rng.Intn(len(model)/4 + 1)
			if got := b.TrimFront(n); got != n {
				t.Fatalf("TrimFront(%d) = %d", n, got)
			}
			model = model[n:]
		case 1:
			drop := map[StreamID]struct{}{}
			var kept []StreamEntry
			for _, e := range model {
				if rng.Intn(5) == 0 {
					drop[e.ID] = struct{}{}
				} else {
					kept = append(kept, e)
				}
			}
			drop[StreamID{Millis: 1 << 40}] = struct{}{} // missing id is ignored
			if got := b.Delete(drop); got != len(model)-len(kept) {
				t.Fatalf("Delete removed %d, want %d", got, len(model)-len(kept))
			}
			model = kept
		case 2:
			if len(model) == 0 {
				break
			}
			min := model[rng.Intn(len(model))].ID
			want := 0
			for _, e := range model {
				if e.ID.less(min) {
					want++
				}
			}
			limit := 0
			if rng.Intn(2) == 0 {
				limit = 1 + rng.Intn(10)
				if want > limit {
					want = limit
				}
			}
			if got := b.CountBefore(min, limit); got != want {
				t.Fatalf("CountBefore = %d, want %d", got, want)
			}
		}
		checkBodyInvariants(t, b)
		if len(b.chunks) > maxChunks {
			maxChunks = len(b.chunks)
		}
		if b.Len() != len(model) {
			t.Fatalf("Len = %d, want %d", b.Len(), len(model))
		}
		if got := b.All(); len(model) > 0 && !reflect.DeepEqual(got, model) {
			t.Fatalf("All differs from model at round %d", round)
		}
		if len(model) > 0 {
			probe := model[rng.Intn(len(model))]
			got, ok := b.Get(probe.ID)
			if !ok || !reflect.DeepEqual(got, probe) || !b.Has(probe.ID) {
				t.Fatalf("Get(%s) = %v %v", probe.ID, got, ok)
			}
			if b.Has(StreamID{Millis: probe.ID.Millis, Sequence: 99}) {
				t.Fatalf("Has found an entry that was never added")
			}
			first, _ := b.First()
			last, _ := b.Last()
			if !reflect.DeepEqual(first, model[0]) || !reflect.DeepEqual(last, model[len(model)-1]) {
				t.Fatalf("First/Last mismatch")
			}
			// Ascending scans, inclusive and exclusive.
			for _, inclusive := range []bool{true, false} {
				var seen []StreamID
				b.ForEachFrom(probe.ID, inclusive, func(e StreamEntry) bool { seen = append(seen, e.ID); return true })
				var want []StreamID
				for _, e := range model {
					if probe.ID.less(e.ID) || inclusive && probe.ID.equal(e.ID) {
						want = append(want, e.ID)
					}
				}
				if !reflect.DeepEqual(seen, want) {
					t.Fatalf("ForEachFrom(%s,%v) = %d ids, want %d", probe.ID, inclusive, len(seen), len(want))
				}
			}
			var rev []StreamID
			b.ForEachReverse(probe.ID, func(e StreamEntry) bool { rev = append(rev, e.ID); return true })
			var wantRev []StreamID
			for i := len(model) - 1; i >= 0; i-- {
				if !probe.ID.less(model[i].ID) {
					wantRev = append(wantRev, model[i].ID)
				}
			}
			if !reflect.DeepEqual(rev, wantRev) {
				t.Fatalf("ForEachReverse(%s) = %d ids, want %d", probe.ID, len(rev), len(wantRev))
			}
		}
	}
	t.Logf("final len=%d chunks=%d maxChunks=%d", b.Len(), len(b.chunks), maxChunks)
	if maxChunks < 3 {
		t.Fatalf("expected the stream to span several chunks, got at most %d", maxChunks)
	}
}

func TestStreamBodyRollbackRestoresEnd(t *testing.T) {
	b := newStreamBody()
	for i := 1; i <= 500; i++ {
		b.Append(StreamID{Millis: uint64(i)}, testStreamFields(i, i))
	}
	b.LastID = StreamID{Millis: 500}
	b.EntriesAdded = 500
	snapshot := b.All()
	chunksBefore := len(b.chunks)
	mark := b.mark()
	for i := 501; i <= 3000; i++ {
		b.Append(StreamID{Millis: uint64(i)}, testStreamFields(i, i))
	}
	if len(b.chunks) == chunksBefore {
		t.Fatalf("test needs the appends to create new chunks")
	}
	b.rollback(mark)
	checkBodyInvariants(t, b)
	if !reflect.DeepEqual(b.All(), snapshot) || b.EntriesAdded != 500 {
		t.Fatalf("rollback did not restore the log")
	}
	// The log keeps accepting appends after a rollback.
	b.Append(StreamID{Millis: 501}, testStreamFields(1, 1))
	checkBodyInvariants(t, b)
}

func TestStreamStoredValueStaysSmall(t *testing.T) {
	s, _ := NewWithShards(4)
	if _, _, err := s.StreamAdd("q", "1-0", streamFields("a", "b"), StreamAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.StreamGroupCreate("q", "g", "0", false, -1); err != nil {
		t.Fatal(err)
	}
	sh := s.shardFor("q")
	e, _ := sh.get("q")
	small := len(sh.encoded(e))
	payload := make([]byte, 200)
	for i := 2; i < 20000; i++ {
		if _, _, err := s.StreamAdd("q", fmt.Sprintf("%d-0", i), []StreamField{{Field: []byte("p"), Value: payload}}, StreamAddOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	e, _ = sh.get("q")
	if got := len(sh.encoded(e)); got != small {
		t.Fatalf("stored value grew from %d to %d bytes while appending", small, got)
	}
	body := s.streamLogs.get("q")
	if body.Len() != 19999 || len(body.chunks) < 50 {
		t.Fatalf("len=%d chunks=%d", body.Len(), len(body.chunks))
	}
	if got := s.Memory().StreamBytes; got != body.memory() {
		t.Fatalf("StreamBytes = %d, want %d", got, body.memory())
	}
}

func TestStreamMemoryIsReleased(t *testing.T) {
	s, _ := NewWithShards(4)
	base := s.Memory().AccountedBytes
	payload := make([]byte, 300)
	for i := 1; i <= 5000; i++ {
		if _, _, err := s.StreamAdd("a", fmt.Sprintf("%d-0", i), []StreamField{{Field: []byte("p"), Value: payload}}, StreamAddOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if s.Memory().StreamBytes == 0 {
		t.Fatal("stream bytes were not accounted")
	}
	// Trimming gives memory back.
	full := s.Memory().StreamBytes
	if n, err := s.StreamTrimMaxLen("a", 100, 0); err != nil || n != 4900 {
		t.Fatalf("trim = %d %v", n, err)
	}
	if got := s.Memory().StreamBytes; got >= full/10 {
		t.Fatalf("stream bytes after trim = %d, before = %d", got, full)
	}
	// Every way of removing the key releases the rest.
	if _, _, err := s.StreamAdd("b", "1-0", streamFields("x", "y"), StreamAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("a", []byte("plain"), 0); err != nil {
		t.Fatal(err)
	}
	if !s.Delete("b") {
		t.Fatal("DEL b failed")
	}
	if got := s.Memory().StreamBytes; got != 0 {
		t.Fatalf("stream bytes after overwrite and delete = %d", got)
	}
	if got := s.Memory().AccountedBytes; got > base+4096 {
		t.Fatalf("accounted bytes %d did not return near the baseline %d", got, base)
	}
	if s.streamLogs.get("a") != nil || s.streamLogs.get("b") != nil {
		t.Fatal("entry logs remain registered")
	}
}

func TestStreamFlushAndMSetDropLogs(t *testing.T) {
	s, _ := NewWithShards(4)
	for _, key := range []string{"x", "y"} {
		if _, _, err := s.StreamAdd(key, "1-0", streamFields("a", "b"), StreamAddOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MSet([]string{"x"}, [][]byte{[]byte("v")}); err != nil {
		t.Fatal(err)
	}
	if s.streamLogs.get("x") != nil {
		t.Fatal("MSET left the entry log of the overwritten stream")
	}
	s.FlushDB()
	if s.streamLogs.get("y") != nil || s.Memory().StreamBytes != 0 {
		t.Fatal("flush left stream state behind")
	}
}

func TestStreamOOMLeavesStreamUnchanged(t *testing.T) {
	s, _ := NewWithShards(4)
	payload := make([]byte, 1000)
	for i := 1; i <= 200; i++ {
		if _, _, err := s.StreamAdd("q", fmt.Sprintf("%d-0", i), []StreamField{{Field: []byte("p"), Value: payload}}, StreamAddOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	s.SetMaxMemory(s.Memory().AccountedBytes + 100)
	_, _, err := s.StreamAdd("q", "999-0", []StreamField{{Field: []byte("p"), Value: payload}}, StreamAddOptions{})
	if err != ErrOOM {
		t.Fatalf("XADD over the limit = %v, want OOM", err)
	}
	if n, _ := s.StreamLen("q"); n != 200 {
		t.Fatalf("length after rejected XADD = %d", n)
	}
	if _, _, err := s.StreamAdd("fresh", "1-0", []StreamField{{Field: []byte("p"), Value: payload}}, StreamAddOptions{}); err != ErrOOM {
		t.Fatalf("creating a stream over the limit = %v, want OOM", err)
	}
	if s.streamLogs.get("fresh") != nil || s.Type("fresh") != "none" {
		t.Fatal("rejected stream creation left state behind")
	}
}

func TestStreamExportRestoreRoundTripLarge(t *testing.T) {
	s, _ := NewWithShards(4)
	for i := 1; i <= 3000; i++ {
		if _, _, err := s.StreamAdd("q", fmt.Sprintf("%d-0", i), testStreamFields(i, i), StreamAddOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []string{"g1", "g2"} {
		if err := s.StreamGroupCreate("q", g, "0", false, -1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.StreamGroupRead([]string{"q"}, "g1", "c", []StreamGroupReadCursor{{New: true}}, 500, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StreamDelete("q", []StreamID{{Millis: 10}, {Millis: 2000}}); err != nil {
		t.Fatal(err)
	}
	records := s.Export([]string{"q"})
	if len(records) != 1 || records[0].ValueType != uint8(TypeStream) {
		t.Fatalf("export = %#v", records)
	}
	other, _ := NewWithShards(4)
	if err := other.Restore(records, false); err != nil {
		t.Fatal(err)
	}
	again := other.Export([]string{"q"})
	if !reflect.DeepEqual(records[0].Value, again[0].Value) {
		t.Fatal("restored stream exports differently")
	}
	a, _, _ := s.StreamSnapshot("q")
	b, _, _ := other.StreamSnapshot("q")
	if !reflect.DeepEqual(a, b) {
		t.Fatal("restored snapshot differs")
	}
	// Restoring over an existing stream replaces its log.
	if err := other.Restore([]persistence.Record{{Key: []byte("q"), Deleted: true}}, false); err != nil {
		t.Fatal(err)
	}
	if other.streamLogs.get("q") != nil || other.Memory().StreamBytes != 0 {
		t.Fatal("deleting through a record kept the log")
	}
	if err := other.Restore(records, false); err != nil {
		t.Fatal(err)
	}
	if err := other.Restore(records, false); err != nil {
		t.Fatal(err)
	}
	if n, _ := other.StreamLen("q"); n != 2998 {
		t.Fatalf("length after repeated restore = %d", n)
	}
	if got, want := other.Memory().StreamBytes, other.streamLogs.get("q").memory(); got != want {
		t.Fatalf("StreamBytes = %d, want %d after repeated restore", got, want)
	}
	// RENAME carries the log along.
	if handled, ok, err := other.RenameStream("q", "q2", false); err != nil || !ok || !handled {
		t.Fatalf("rename = %v %v %v", handled, ok, err)
	}
	if other.streamLogs.get("q") != nil || other.streamLogs.get("q2") == nil {
		t.Fatal("rename did not move the log")
	}
	if got, want := other.Memory().StreamBytes, other.streamLogs.get("q2").memory(); got != want {
		t.Fatalf("StreamBytes = %d, want %d after rename", got, want)
	}
}

func TestStreamPolicyTrimIsAtomicOnPublishFailure(t *testing.T) {
	s, _ := NewWithShards(4)
	for i := 1; i <= 50; i++ {
		if _, _, err := s.StreamAdd("q", fmt.Sprintf("%d-0", i), streamFields("a", "b"), StreamAddOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.StreamGroupCreate("q", "g", "0", false, -1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StreamGroupRead([]string{"q"}, "g", "c", []StreamGroupReadCursor{{New: true}}, 10, false); err != nil {
		t.Fatal(err)
	}
	// DELREF drops the pending references of the entries it trims.
	if n, err := s.StreamTrimMaxLenWithPolicy("q", 40, 0, StreamRefDelete); err != nil || n != 10 {
		t.Fatalf("trim = %d %v", n, err)
	}
	pending, err := s.StreamGroupPendingSummary("q", "g")
	if err != nil || pending.Count != 0 {
		t.Fatalf("pending after DELREF trim = %+v %v", pending, err)
	}
	// ACKED keeps entries that some group has not acknowledged.
	if _, err := s.StreamGroupRead([]string{"q"}, "g", "c", []StreamGroupReadCursor{{New: true}}, 5, false); err != nil {
		t.Fatal(err)
	}
	if n, err := s.StreamTrimMaxLenWithPolicy("q", 10, 0, StreamRefAcked); err != nil || n != 0 {
		t.Fatalf("ACKED trim removed %d (%v), want 0 while entries are pending", n, err)
	}
	ids := []StreamID{}
	infos, _ := s.StreamGroupPendingRange("q", "g", StreamRangeBound{}, StreamRangeBound{ID: StreamID{Millis: ^uint64(0), Sequence: ^uint64(0)}}, 100, "", 0)
	for _, info := range infos {
		ids = append(ids, info.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].less(ids[j]) })
	if acked, err := s.StreamGroupAck("q", "g", ids); err != nil || acked != int64(len(ids)) {
		t.Fatalf("ack = %d %v", acked, err)
	}
	if n, err := s.StreamTrimMaxLenWithPolicy("q", 10, 0, StreamRefAcked); err != nil || n == 0 {
		t.Fatalf("ACKED trim after acknowledgement removed %d (%v)", n, err)
	}
}

func BenchmarkStreamAdd(b *testing.B) {
	for _, existing := range []int{0, 10000, 100000} {
		b.Run(fmt.Sprintf("existing=%d", existing), func(b *testing.B) {
			s, _ := NewWithShards(4)
			payload := make([]byte, 200)
			fields := []StreamField{{Field: []byte("p"), Value: payload}}
			next := uint64(1)
			for i := 0; i < existing; i++ {
				if _, _, err := s.StreamAdd("q", fmt.Sprintf("%d-0", next), fields, StreamAddOptions{}); err != nil {
					b.Fatal(err)
				}
				next++
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, err := s.StreamAdd("q", fmt.Sprintf("%d-0", next), fields, StreamAddOptions{}); err != nil {
					b.Fatal(err)
				}
				next++
			}
		})
	}
}
