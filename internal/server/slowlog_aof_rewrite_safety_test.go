package server

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

type pausedSlowlogRewriteJournal struct {
	log     *persistence.Log
	entered chan struct{}
	release chan struct{}
}

func (j *pausedSlowlogRewriteJournal) Append(records []persistence.Record) error {
	return j.log.Append(records)
}

func (j *pausedSlowlogRewriteJournal) Rewrite(records []persistence.Record) error {
	close(j.entered)
	<-j.release
	return j.log.Rewrite(records)
}

func (j *pausedSlowlogRewriteJournal) BeginRewrite() error {
	return j.log.BeginRewrite()
}

func (j *pausedSlowlogRewriteJournal) FinishRewrite(records []persistence.Record) error {
	close(j.entered)
	<-j.release
	return j.log.FinishRewrite(records)
}

func TestBGRewriteAOFAllowsWritesDuringBackgroundRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active.aof")
	log, err := persistence.Open(path, "always")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	j := &pausedSlowlogRewriteJournal{
		log: log, entered: make(chan struct{}), release: make(chan struct{}),
	}
	var once sync.Once
	release := func() { once.Do(func() { close(j.release) }) }
	defer release()
	s := New(engine.New())
	s.SetJournal(j)
	execute(t, s, "SET", "before", "one")
	execute(t, s, "BGREWRITEAOF")

	select {
	case <-j.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("rewrite did not enter background disk phase")
	}

	if !s.durableMu.TryLock() {
		t.Fatal("buffered active rewrite must not hold durability lock during background disk phase")
	}
	s.durableMu.Unlock()

	done := make(chan error, 1)
	go func() {
		_, err := s.Execute(clientArgs("SET", "during", "two"))
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("durable write blocked behind background AOF rewrite")
	}

	release()
	s.waitPersistenceJobs()

	values := make(map[string]string)
	if err := persistence.Replay(path, func(records []persistence.Record) error {
		for _, record := range records {
			if record.Reset {
				values = make(map[string]string)
			} else if record.Deleted {
				delete(values, string(record.Key))
			} else {
				values[string(record.Key)] = string(record.Value)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if values["before"] != "one" || values["during"] != "two" {
		t.Fatalf("replayed values=%v", values)
	}
}
