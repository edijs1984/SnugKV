package server

import (
	"path/filepath"
	"strings"
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

func TestBGRewriteAOFScheduleAfterSave(t *testing.T) {
	for _, failSave := range []bool{false, true} {
		name := "successful-save"
		if failSave {
			name = "failed-save"
		}
		t.Run(name, func(t *testing.T) {
			s := New(engine.New())
			dir := t.TempDir()
			s.snapshotPath = filepath.Join(dir, "dump.snap")
			if failSave {
				s.snapshotPath = filepath.Join(dir, "missing", "dump.snap")
			}
			s.aofRewritePath = filepath.Join(dir, "export.aof")
			execute(t, s, "SET", "queued", "value")
			// Hold the save in its running state deterministically, then
			// invoke the real save worker to exercise completion and handoff.
			s.persistenceJobMu.Lock()
			s.bgsaveRunning = true
			s.persistenceJobMu.Unlock()
			for i := 0; i < 2; i++ {
				got := execute(t, s, "BGREWRITEAOF")
				if got != "+Background append only file rewriting scheduled\r\n" {
					t.Fatalf("BGREWRITEAOF=%q", got)
				}
			}
			requirePersistenceStatus(t, s, "aof_rewrite_scheduled:1", "aof_rewrite_in_progress:0")
			s.runBackgroundSave()
			waitPersistenceStatusJob(t, s)
			requirePersistenceStatus(t, s, "aof_rewrite_scheduled:0", "aof_rewrite_in_progress:0",
				"aof_last_bgrewrite_status:ok", "aof_enabled:0")
			found := false
			if err := persistence.Replay(s.aofRewritePath, func(records []persistence.Record) error {
				for _, record := range records {
					if string(record.Key) == "queued" && string(record.Value) == "value" {
						found = true
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if !found {
				t.Fatal("queued rewrite did not persist data")
			}
		})
	}
}


func TestPersistenceJobsWaitForActiveRewrite(t *testing.T) {
	dir := t.TempDir()
	log, err := persistence.Open(filepath.Join(dir, "active.aof"), "always")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	j := &pausedSlowlogRewriteJournal{
		log: log,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	s := New(engine.New())
	s.SetJournal(j)
	execute(t, s, "SET", "before", "value")
	execute(t, s, "BGREWRITEAOF")

	select {
	case <-j.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("rewrite did not start")
	}

	waited := make(chan struct{})
	go func() {
		s.waitPersistenceJobs()
		close(waited)
	}()

	select {
	case <-waited:
		t.Fatal("persistence wait returned while rewrite was still running")
	case <-time.After(50 * time.Millisecond):
	}

	close(j.release)
	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		t.Fatal("persistence wait did not return after rewrite completed")
	}
}


func TestPersistenceJobsWaitThroughRewriteToScheduledSave(t *testing.T) {
	dir := t.TempDir()
	log, err := persistence.Open(filepath.Join(dir, "active.aof"), "always")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	j := &pausedSlowlogRewriteJournal{
		log: log,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	s := New(engine.New())
	s.snapshotPath = filepath.Join(dir, "dump.snap")
	s.SetJournal(j)
	execute(t, s, "SET", "queued", "value")
	execute(t, s, "BGREWRITEAOF")

	select {
	case <-j.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("rewrite did not start")
	}

	got, err := s.executeSave(true, true)
	if err != nil || string(got) != "+Background saving scheduled\r\n" {
		t.Fatalf("BGSAVE SCHEDULE=%q err=%v", got, err)
	}

	waited := make(chan struct{})
	go func() {
		s.waitPersistenceJobs()
		close(waited)
	}()

	close(j.release)

	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		t.Fatal("persistence wait did not include scheduled save handoff")
	}

	found := false
	if err := persistence.ReplaySnapshot(s.snapshotPath, func(records []persistence.Record) error {
		for _, record := range records {
			if string(record.Key) == "queued" && string(record.Value) == "value" {
				found = true
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("scheduled save had not completed when persistence wait returned")
	}
}

func TestPersistenceJobsWaitThroughSaveToScheduledRewrite(t *testing.T) {
	s := New(engine.New())
	dir := t.TempDir()
	s.snapshotPath = filepath.Join(dir, "dump.snap")
	s.aofRewritePath = filepath.Join(dir, "export.aof")
	execute(t, s, "SET", "queued", "value")

	s.persistenceJobMu.Lock()
	s.bgsaveRunning = true
	s.persistenceJobMu.Unlock()

	got := execute(t, s, "BGREWRITEAOF")
	if got != "+Background append only file rewriting scheduled\r\n" {
		t.Fatalf("BGREWRITEAOF=%q", got)
	}

	s.persistenceJobs.Add(1)
	go func() {
		defer s.persistenceJobs.Done()
		s.runBackgroundSave()
	}()

	waited := make(chan struct{})
	go func() {
		s.waitPersistenceJobs()
		close(waited)
	}()

	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		t.Fatal("persistence wait did not include scheduled rewrite handoff")
	}

	found := false
	if err := persistence.Replay(s.aofRewritePath, func(records []persistence.Record) error {
		for _, record := range records {
			if string(record.Key) == "queued" && string(record.Value) == "value" {
				found = true
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("scheduled rewrite had not completed when persistence wait returned")
	}
}


func TestSaveRejectsActiveBGSave(t *testing.T) {
	s := New(engine.New())
	s.snapshotPath = filepath.Join(t.TempDir(), "dump.snap")
	s.persistenceJobMu.Lock()
	s.bgsaveRunning = true
	s.persistenceJobMu.Unlock()

	_, err := s.executeSave(false, false)
	if err == nil || err.Error() != "ERR Background save already in progress" {
		t.Fatalf("SAVE during BGSAVE err=%v", err)
	}
}


func TestBGSaveRejectsActiveSave(t *testing.T) {
	s := New(engine.New())
	s.snapshotPath = filepath.Join(t.TempDir(), "dump.snap")
	s.persistenceJobMu.Lock()
	s.saveRunning = true
	s.persistenceJobMu.Unlock()

	for _, schedule := range []bool{false, true} {
		_, err := s.executeSave(true, schedule)
		if err == nil || err.Error() != "ERR Background save already in progress" {
			t.Fatalf("BGSAVE schedule=%v during SAVE err=%v", schedule, err)
		}
	}
	if s.bgsaveScheduled {
		t.Fatal("BGSAVE SCHEDULE must not queue behind synchronous SAVE")
	}
}


func TestTransactionBGSAVECapturesStateAtCommandPosition(t *testing.T) {
	s := New(engine.New())
	s.snapshotPath = filepath.Join(t.TempDir(), "dump.snap")

	tx := newTransactionSession(s)
	if handled, got, err := tx.handleCommand(clientArgs("MULTI")); !handled || err != nil || string(got) != "+OK\r\n" {
		t.Fatalf("MULTI handled=%v got=%q err=%v", handled, got, err)
	}
	for _, args := range [][][]byte{
		clientArgs("SET", "before", "one"),
		clientArgs("BGSAVE"),
		clientArgs("SET", "after", "two"),
	} {
		if handled, got, err := tx.handleCommand(args); !handled || err != nil || string(got) != "+QUEUED\r\n" {
			t.Fatalf("queue %q handled=%v got=%q err=%v", args[0], handled, got, err)
		}
	}

	got, err := tx.exec()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "+Background saving started\r\n") {
		t.Fatalf("EXEC response=%q", got)
	}
	s.waitPersistenceJobs()

	values := map[string]string{}
	if err := persistence.ReplaySnapshot(s.snapshotPath, func(records []persistence.Record) error {
		for _, record := range records {
			if record.Deleted || record.Reset {
				continue
			}
			values[string(record.Key)] = string(record.Value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if values["before"] != "one" {
		t.Fatalf("snapshot missing pre-BGSAVE write: %v", values)
	}
	if _, ok := values["after"]; ok {
		t.Fatalf("snapshot included post-BGSAVE transaction write: %v", values)
	}
	if got := execute(t, s, "GET", "after"); got != "$3\r\ntwo\r\n" {
		t.Fatalf("live state missing post-BGSAVE write: %q", got)
	}
}


func TestTransactionBGRewriteAOFIncludesLaterTransactionWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "active.aof")
	journal, err := persistence.Open(path, "always")
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()

	s := New(engine.New())
	s.SetJournal(journal)

	tx := newTransactionSession(s)
	if handled, got, err := tx.handleCommand(clientArgs("MULTI")); !handled || err != nil || string(got) != "+OK\r\n" {
		t.Fatalf("MULTI handled=%v got=%q err=%v", handled, got, err)
	}
	for _, args := range [][][]byte{
		clientArgs("SET", "before", "one"),
		clientArgs("BGREWRITEAOF"),
		clientArgs("SET", "after", "two"),
	} {
		if handled, got, err := tx.handleCommand(args); !handled || err != nil || string(got) != "+QUEUED\r\n" {
			t.Fatalf("queue %q handled=%v got=%q err=%v", args[0], handled, got, err)
		}
	}

	done := make(chan struct{})
	var execReply []byte
	var execErr error
	go func() {
		execReply, execErr = tx.exec()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("EXEC deadlocked with BGREWRITEAOF")
	}
	if execErr != nil {
		t.Fatal(execErr)
	}
	if !strings.Contains(string(execReply), "+Background append only file rewriting started\r\n") {
		t.Fatalf("EXEC response=%q", execReply)
	}

	s.waitPersistenceJobs()

	values := map[string]string{}
	if err := persistence.Replay(path, func(records []persistence.Record) error {
		for _, record := range records {
			if record.Reset {
				values = map[string]string{}
				continue
			}
			if record.Deleted {
				delete(values, string(record.Key))
				continue
			}
			values[string(record.Key)] = string(record.Value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if values["before"] != "one" || values["after"] != "two" {
		t.Fatalf("rewritten AOF lost transaction writes: %v", values)
	}
}
