package server

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

func waitPersistenceStatusJob(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.persistenceJobMu.Lock()
		running := s.bgsaveRunning || s.aofRewriteRunning
		s.persistenceJobMu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("background persistence job timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func requirePersistenceStatus(t *testing.T, s *Server, fields ...string) {
	t.Helper()
	got := execute(t, s, "INFO", "persistence")
	for _, field := range fields {
		if !strings.Contains(got, field+"\r\n") {
			t.Fatalf("missing %q in INFO: %q", field, got)
		}
	}
}

func TestPersistenceInfoSections(t *testing.T) {
	s := New(engine.New())
	for _, section := range []string{"persistence", "default", "all"} {
		if got := execute(t, s, "INFO", section); !strings.Contains(got, "# Persistence\r\n") {
			t.Fatalf("INFO %s missing persistence: %q", section, got)
		}
	}
	requirePersistenceStatus(t, s, "aof_enabled:0", "rdb_bgsave_in_progress:0",
		"aof_rewrite_in_progress:0", "rdb_last_bgsave_status:ok", "aof_last_bgrewrite_status:ok")
	s.persistenceJobMu.Lock()
	s.bgsaveRunning, s.aofRewriteRunning = true, true
	s.persistenceJobMu.Unlock()
	requirePersistenceStatus(t, s, "rdb_bgsave_in_progress:1", "aof_rewrite_in_progress:1")
}

func TestPersistenceInfoBGSaveFailureThenSuccess(t *testing.T) {
	s := New(engine.New())
	dir := t.TempDir()
	s.snapshotPath = filepath.Join(dir, "missing", "dump.snap")
	before := s.lastSaveUnix.Load()
	execute(t, s, "BGSAVE")
	waitPersistenceStatusJob(t, s)
	requirePersistenceStatus(t, s, "rdb_last_bgsave_status:err", "rdb_bgsave_in_progress:0")
	if s.lastSaveUnix.Load() != before {
		t.Fatal("failed save changed LASTSAVE")
	}
	s.snapshotPath = filepath.Join(dir, "dump.snap")
	execute(t, s, "BGSAVE")
	waitPersistenceStatusJob(t, s)
	requirePersistenceStatus(t, s, "rdb_last_bgsave_status:ok", "rdb_bgsave_in_progress:0")
}

type statusRewriteJournal struct { err error }

func (j *statusRewriteJournal) Append([]persistence.Record) error { return nil }
func (j *statusRewriteJournal) Rewrite([]persistence.Record) error { return j.err }

func TestPersistenceInfoAOFRewriteFailureThenSuccess(t *testing.T) {
	s := New(engine.New())
	j := &statusRewriteJournal{err: errors.New("injected rewrite failure")}
	s.SetJournal(j)
	execute(t, s, "BGREWRITEAOF")
	waitPersistenceStatusJob(t, s)
	requirePersistenceStatus(t, s, "aof_enabled:1", "aof_rewrite_in_progress:0", "aof_last_bgrewrite_status:err")
	j.err = nil
	execute(t, s, "BGREWRITEAOF")
	waitPersistenceStatusJob(t, s)
	requirePersistenceStatus(t, s, "aof_enabled:1", "aof_rewrite_in_progress:0", "aof_last_bgrewrite_status:ok")
}

func TestPersistenceInfoOneOffRewriteStaysDisabled(t *testing.T) {
	s := New(engine.New())
	s.aofRewritePath = filepath.Join(t.TempDir(), "export.aof")
	execute(t, s, "BGREWRITEAOF")
	waitPersistenceStatusJob(t, s)
	requirePersistenceStatus(t, s, "aof_enabled:0", "aof_rewrite_in_progress:0", "aof_last_bgrewrite_status:ok")
}
