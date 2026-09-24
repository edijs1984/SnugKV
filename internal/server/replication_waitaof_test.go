package server

import (
	"errors"
	"sync"
	"testing"
	"time"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

type waitAOFJournal struct {
	mu       sync.Mutex
	appended uint64
	synced   uint64
	changed  chan struct{}
}

func newWaitAOFJournal() *waitAOFJournal {
	return &waitAOFJournal{changed: make(chan struct{})}
}

func (j *waitAOFJournal) Append([]persistence.Record) error {
	j.mu.Lock()
	j.appended++
	j.mu.Unlock()
	return nil
}

func (j *waitAOFJournal) DurabilitySnapshot() (uint64, uint64, <-chan struct{}) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.appended, j.synced, j.changed
}

func (j *waitAOFJournal) syncAll() {
	j.mu.Lock()
	if j.synced < j.appended {
		j.synced = j.appended
		close(j.changed)
		j.changed = make(chan struct{})
	}
	j.mu.Unlock()
}

func TestWaitAOFArgumentValidationMatchesRedis(t *testing.T) {
	s := New(engine.New())

	cases := []struct {
		args [][]byte
		want string
	}{
		{[][]byte{[]byte("WAITAOF"), []byte("2"), []byte("0"), []byte("100")}, "ERR value is out of range, value must between 0 and 1"},
		{[][]byte{[]byte("WAITAOF"), []byte("-1"), []byte("0"), []byte("100")}, "ERR value is out of range, value must between 0 and 1"},
		{[][]byte{[]byte("WAITAOF"), []byte("0"), []byte("-1"), []byte("100")}, "ERR value is out of range, must be positive"},
		{[][]byte{[]byte("WAITAOF"), []byte("0"), []byte("1"), []byte("-1")}, "ERR timeout is negative"},
		{[][]byte{[]byte("WAITAOF"), []byte("foo"), []byte("0"), []byte("100")}, "ERR value is not an integer or out of range"},
		{[][]byte{[]byte("WAITAOF"), []byte("0"), []byte("foo"), []byte("100")}, "ERR value is not an integer or out of range"},
		{[][]byte{[]byte("WAITAOF"), []byte("0"), []byte("0"), []byte("foo")}, "ERR timeout is not an integer or out of range"},
	}
	for _, tc := range cases {
		_, err := s.executeWaitAOF(tc.args, 0, 0, nil, false)
		if err == nil || err.Error() != tc.want {
			t.Fatalf("WAITAOF %q err=%v want=%q", tc.args, err, tc.want)
		}
	}
}

func TestWaitAOFLocalRequiresAOF(t *testing.T) {
	s := New(engine.New())
	_, err := s.executeWaitAOF(
		[][]byte{[]byte("WAITAOF"), []byte("1"), []byte("0"), []byte("100")},
		0, 0, nil, false,
	)
	if err == nil || err.Error() != "ERR WAITAOF cannot be used when numlocal is set but appendonly is disabled." {
		t.Fatalf("err=%v", err)
	}
}

func TestWaitAOFLocalWakesAfterRealSyncProgress(t *testing.T) {
	s := New(engine.New())
	journal := newWaitAOFJournal()
	s.SetJournal(journal)

	var replOffset int64
	var durabilitySequence uint64
	if _, err := s.executeForSessionCaptureState(
		[][]byte{[]byte("SET"), []byte("waitaof:key"), []byte("value")},
		nil,
		&replOffset,
		&durabilitySequence,
	); err != nil {
		t.Fatal(err)
	}
	if durabilitySequence == 0 {
		t.Fatal("write did not capture AOF sequence")
	}

	done := make(chan struct {
		reply string
		err   error
	}, 1)
	go func() {
		reply, err := s.executeWaitAOF(
			[][]byte{[]byte("WAITAOF"), []byte("1"), []byte("0"), []byte("1000")},
			replOffset,
			durabilitySequence,
			nil,
			true,
		)
		done <- struct {
			reply string
			err   error
		}{string(reply), err}
	}()

	select {
	case result := <-done:
		t.Fatalf("WAITAOF returned before fsync: reply=%q err=%v", result.reply, result.err)
	case <-time.After(20 * time.Millisecond):
	}

	journal.syncAll()

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.reply != "*2\r\n:1\r\n:0\r\n" {
			t.Fatalf("reply=%q", result.reply)
		}
	case <-time.After(time.Second):
		t.Fatal("WAITAOF did not wake on fsync progress")
	}
}

func TestWaitAOFReplicaFACKCount(t *testing.T) {
	s := New(engine.New())
	id, _, _ := s.replication.registerReplica(func([]byte) error { return nil })
	defer s.replication.unregisterReplica(id)

	s.replication.acknowledgeReplica(id, 80, 75)

	got, err := s.executeWaitAOF(
		[][]byte{[]byte("WAITAOF"), []byte("0"), []byte("1"), []byte("0")},
		75, 0, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "*2\r\n:0\r\n:1\r\n" {
		t.Fatalf("reply=%q", got)
	}
}

func TestWaitAOFInsideExecDoesNotBlock(t *testing.T) {
	s := New(engine.New())
	session := newTransactionSession(s)
	session.multi = true
	session.waitTargetOffset = 100
	session.waitAOFSequence = 5
	session.queue = [][][]byte{
		{[]byte("WAITAOF"), []byte("0"), []byte("1"), []byte("10000")},
	}

	started := time.Now()
	got, err := session.exec()
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("WAITAOF inside EXEC blocked for %v", elapsed)
	}
	if string(got) != "*1\r\n*2\r\n:0\r\n:0\r\n" {
		t.Fatalf("EXEC WAITAOF=%q", got)
	}
}

func TestWaitAOFCancel(t *testing.T) {
	s := New(engine.New())
	cancel := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := s.executeWaitAOF(
			[][]byte{[]byte("WAITAOF"), []byte("0"), []byte("1"), []byte("0")},
			10, 0, cancel, true,
		)
		done <- err
	}()
	close(cancel)
	select {
	case err := <-done:
		if !errors.Is(err, errBlockingClientGone) {
			t.Fatalf("cancel err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WAITAOF did not cancel")
	}
}

func TestSnugReplicaFrameMustPersistBeforeFACK(t *testing.T) {
	s := New(engine.New())
	journal := newWaitAOFJournal()
	s.SetJournal(journal)

	records := []persistence.Record{{Key: []byte("replica:key"), Value: []byte("value")}}
	s.durableMu.Lock()
	err := s.applySnugReplicationRecordsLocked(records)
	s.durableMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	s.noteReplicaAOFOffset(123)

	if offset, ok := s.replicaAOFFsyncedOffset(); ok {
		t.Fatalf("FACK advanced before fsync: offset=%d", offset)
	}

	journal.syncAll()

	offset, ok := s.replicaAOFFsyncedOffset()
	if !ok || offset != 123 {
		t.Fatalf("FACK after fsync offset=%d ok=%v", offset, ok)
	}

	value, found, wrong := s.store.GetString("replica:key")
	if !found || wrong || string(value) != "value" {
		t.Fatalf("replicated value found=%v wrong=%v value=%q", found, wrong, value)
	}
}
