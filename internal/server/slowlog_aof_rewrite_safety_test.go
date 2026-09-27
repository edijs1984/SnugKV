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

func TestBGRewriteAOFSerializesActiveWrites(t *testing.T) {
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
		t.Fatal("rewrite did not start")
	}
	if s.durableMu.TryLock() {
		s.durableMu.Unlock()
		t.Fatal("active rewrite must hold durability lock across export and replacement")
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.Execute(clientArgs("SET", "after", "two"))
		done <- err
	}()
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("write did not resume after rewrite")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.persistenceJobMu.Lock()
		running := s.aofRewriteRunning
		s.persistenceJobMu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rewrite did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	values := make(map[string]string)
	if err := persistence.Replay(path, func(records []persistence.Record) error {
		for _, record := range records {
			if record.Reset {
				values = make(map[string]string)
			} else {
				values[string(record.Key)] = string(record.Value)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if values["before"] != "one" || values["after"] != "two" {
		t.Fatalf("replayed values=%v", values)
	}
}
