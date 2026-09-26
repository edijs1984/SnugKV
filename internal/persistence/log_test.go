package persistence

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestFramesAndTruncation(t *testing.T) {
	var b bytes.Buffer
	b.WriteString(magic)
	first := []Record{{Key: []byte{255, 0}, Value: []byte("x\r\n"), ExpiresAtMS: 123}}
	WriteFrame(&b, first)
	boundary := b.Len()
	WriteFrame(&b, []Record{{Key: []byte("k"), Deleted: true}})
	data := b.Bytes()
	for n := boundary; n < len(data); n++ {
		count := 0
		offset, err := Read(bytes.NewReader(data[:n]), func(records []Record) error {
			count++
			if !bytes.Equal(records[0].Key, first[0].Key) {
				t.Fatal("binary key")
			}
			return nil
		})
		if err != nil || count != 1 || offset != int64(boundary) {
			t.Fatalf("cut %d: %d %d %v", n, count, offset, err)
		}
	}
	bad := bytes.Clone(data)
	bad[boundary-1] ^= 1
	if _, err := Read(bytes.NewReader(bad), func([]Record) error { return nil }); err == nil {
		t.Fatal("checksum corruption accepted")
	}
}
func TestLogAndSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aof")
	l, err := Open(path, "always")
	if err != nil {
		t.Fatal(err)
	}
	if err = l.Append([]Record{{Key: []byte("k"), Value: []byte("v")}}); err != nil {
		t.Fatal(err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	f.Write([]byte{1, 2, 3})
	f.Close()
	l, err = Open(path, "no")
	if err != nil {
		t.Fatal(err)
	}
	l.Append([]Record{{Key: []byte("k"), Deleted: true}})
	l.Close()
	count := 0
	if err = Replay(path, func([]Record) error { count++; return nil }); err != nil || count != 2 {
		t.Fatalf("replay %d %v", count, err)
	}
	snap := filepath.Join(dir, "snapshot")
	if err = Snapshot(snap, []Record{{Key: []byte("k"), Value: []byte("v")}}); err != nil {
		t.Fatal(err)
	}
	if err = Replay(snap, func([]Record) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }
func TestWriteFailure(t *testing.T) {
	if err := WriteFrame(failedWriter{}, []Record{{}}); err == nil {
		t.Fatal("ignored write failure")
	}
	if _, err := Read(bytes.NewReader([]byte(magic)), func([]Record) error { return io.ErrClosedPipe }); err != nil {
		t.Fatal(err)
	}
}
func FuzzRead(f *testing.F) {
	f.Add([]byte(magic))
	f.Add([]byte("MCLOG999"))
	f.Fuzz(func(t *testing.T, data []byte) { Read(bytes.NewReader(data), func([]Record) error { return nil }) })
}

func TestSnapshotRejectsTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snap")
	if err := Snapshot(path, []Record{{Key: []byte("k"), Value: []byte("v")}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if err := ReplaySnapshot(path, func([]Record) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{len(magic), len(data) - 1, len(data) - 10} {
		os.WriteFile(path, data[:n], 0600)
		if err := ReplaySnapshot(path, func([]Record) error { return nil }); err == nil {
			t.Fatalf("accepted snapshot truncated at %d", n)
		}
	}
}

func TestExclusiveLockAndRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aof")
	log, err := Open(path, "always")
	if err != nil {
		t.Fatal(err)
	}
	if duplicate, err := Open(path, "no"); err == nil {
		duplicate.Close()
		t.Fatal("second writer acquired lock")
	}
	log.Append([]Record{{Key: []byte("old"), Value: []byte("v")}})
	if err = log.Rewrite([]Record{{Key: []byte("new"), Value: []byte("x")}}); err != nil {
		t.Fatal(err)
	}
	if err = log.Append([]Record{{Key: []byte("after"), Value: []byte("y")}}); err != nil {
		t.Fatal(err)
	}
	log.Close()
	seenReset := false
	keys := make(map[string]bool)
	if err = Replay(path, func(records []Record) error {
		for _, r := range records {
			if r.Reset {
				seenReset = true
				clear(keys)
			} else {
				keys[string(r.Key)] = true
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !seenReset || keys["old"] || !keys["new"] || !keys["after"] {
		t.Fatal("rewrite state", keys)
	}
}


func TestMixedJSONAndPlainSetFramesReplay(t *testing.T) {
	var b bytes.Buffer
	b.WriteString(magic)

	if err := WriteFrame(&b, []Record{{Key: []byte("legacy"), Value: []byte("json")}}); err != nil {
		t.Fatal(err)
	}
	if err := WritePlainSetFrame(&b, []byte{0xff, 0x00, 'k'}, []byte("binary-value")); err != nil {
		t.Fatal(err)
	}

	var got []Record
	if _, err := Read(bytes.NewReader(b.Bytes()), func(records []Record) error {
		got = append(got, records...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 {
		t.Fatalf("records=%d want=2", len(got))
	}
	if string(got[0].Key) != "legacy" || string(got[0].Value) != "json" {
		t.Fatalf("legacy record=%+v", got[0])
	}
	if !bytes.Equal(got[1].Key, []byte{0xff, 0x00, 'k'}) ||
		string(got[1].Value) != "binary-value" {
		t.Fatalf("plain SET record=%+v", got[1])
	}
}

func TestLogAppendPlainSetRestartRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aof")
	log, err := Open(path, "always")
	if err != nil {
		t.Fatal(err)
	}
	if err := log.AppendPlainSet([]byte("key"), []byte("value")); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	var got []Record
	if err := Replay(path, func(records []Record) error {
		got = append(got, records...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || string(got[0].Key) != "key" || string(got[0].Value) != "value" {
		t.Fatalf("replayed=%+v", got)
	}
}


func TestPlainSetBatchFrameReplay(t *testing.T) {
	var b bytes.Buffer
	b.WriteString(magic)

	keys := [][]byte{[]byte("a"), []byte{0xff, 0x00, 'b'}, []byte("c")}
	values := [][]byte{[]byte("one"), []byte("two"), []byte("three")}
	if err := WritePlainSetBatchFrame(&b, keys, values); err != nil {
		t.Fatal(err)
	}

	var got []Record
	if _, err := Read(bytes.NewReader(b.Bytes()), func(records []Record) error {
		got = append(got, records...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(keys) {
		t.Fatalf("records=%d want=%d", len(got), len(keys))
	}
	for i := range keys {
		if !bytes.Equal(got[i].Key, keys[i]) || !bytes.Equal(got[i].Value, values[i]) {
			t.Fatalf("record %d=%+v", i, got[i])
		}
	}
}

func TestLogAppendPlainSetBatchDurabilitySequence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aof")
	log, err := Open(path, "no")
	if err != nil {
		t.Fatal(err)
	}
	keys := [][]byte{[]byte("a"), []byte("b"), []byte("c")}
	values := [][]byte{[]byte("1"), []byte("2"), []byte("3")}
	if err := log.AppendPlainSetBatch(keys, values); err != nil {
		t.Fatal(err)
	}
	appended, synced, _ := log.DurabilitySnapshot()
	if appended != 3 || synced != 0 {
		t.Fatalf("durability appended=%d synced=%d", appended, synced)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	var got int
	if err := Replay(path, func(records []Record) error {
		got += len(records)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got != 3 {
		t.Fatalf("replayed records=%d want=3", got)
	}
}
