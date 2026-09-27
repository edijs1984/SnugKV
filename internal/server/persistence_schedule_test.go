package server

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

func TestBGSaveScheduleRejectsRunningSave(t *testing.T) {
	s := New(engine.New())
	s.bgsaveRunning = true
	for _, schedule := range []bool{false, true} {
		_, err := s.executeSave(true, schedule)
		if err == nil || err.Error() != "ERR Background save already in progress" {
			t.Fatalf("schedule=%v err=%v", schedule, err)
		}
	}
	if s.bgsaveScheduled {
		t.Fatal("running save must not queue another save")
	}
}

func TestBGSaveScheduleRunsAfterRewrite(t *testing.T) {
	dir := t.TempDir()
	journal, err := persistence.Open(filepath.Join(dir, "active.aof"), "always")
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	j := &pausedSlowlogRewriteJournal{
		log: journal, entered: make(chan struct{}), release: make(chan struct{}),
	}
	var once sync.Once
	release := func() { once.Do(func() { close(j.release) }) }
	defer release()
	s := New(engine.New())
	s.snapshotPath = filepath.Join(dir, "dump.snap")
	s.SetJournal(j)
	execute(t, s, "SET", "scheduled", "value")
	execute(t, s, "BGREWRITEAOF")
	select {
	case <-j.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("rewrite did not start")
	}
	// The active rewrite owns durableMu, so exercise the job scheduler
	// directly. TCP commands currently wait behind this lock.
	_, err = s.executeSave(true, false)
	want := "ERR Another child process is active (AOF?): can't BGSAVE right now. Use BGSAVE SCHEDULE in order to schedule a BGSAVE whenever possible."
	if err == nil || err.Error() != want {
		t.Fatalf("BGSAVE during rewrite err=%v", err)
	}
	for i := 0; i < 2; i++ {
		got, err := s.executeSave(true, true)
		if err != nil || string(got) != "+Background saving scheduled\r\n" {
			t.Fatalf("SCHEDULE=%q err=%v", got, err)
		}
	}
	release()
	waitPersistenceStatusJob(t, s)
	found := false
	if err := persistence.ReplaySnapshot(s.snapshotPath, func(records []persistence.Record) error {
		for _, record := range records {
			if string(record.Key) == "scheduled" && string(record.Value) == "value" {
				found = true
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("scheduled save did not persist data")
	}
	s.persistenceJobMu.Lock()
	pending := s.bgsaveScheduled
	s.persistenceJobMu.Unlock()
	if pending {
		t.Fatal("completed save left pending flag")
	}
	requirePersistenceStatus(t, s, "rdb_last_bgsave_status:ok", "rdb_bgsave_in_progress:0")
}
