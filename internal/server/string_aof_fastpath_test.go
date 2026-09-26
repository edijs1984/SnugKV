package server

import (
	"fmt"
	"errors"
	"path/filepath"
	"testing"
	"sync"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

type failingSetJournal struct{}

func (failingSetJournal) Append([]persistence.Record) error {
	return errors.New("forced append failure")
}

func TestPlainSetAOFFastPathRestartRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.snug")

	store := engine.New()
	journal, err := persistence.Open(path, "always")
	if err != nil {
		t.Fatal(err)
	}

	srv := New(store)
	srv.SetJournal(journal)

	if got := execute(t, srv, "SET", "key", "value"); got != "+OK\r\n" {
		t.Fatalf("SET=%q", got)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := engine.New()
	if err := persistence.Replay(path, func(records []persistence.Record) error {
		return restarted.Restore(records, false)
	}); err != nil {
		t.Fatal(err)
	}

	restartedSrv := New(restarted)
	if got := execute(t, restartedSrv, "GET", "key"); got != "$5\r\nvalue\r\n" {
		t.Fatalf("GET after restart=%q", got)
	}
}

func TestPlainSetAOFFastPathDoesNotMutateOnAppendFailure(t *testing.T) {
	store := engine.New()
	srv := New(store)

	if got := execute(t, srv, "SET", "key", "old"); got != "+OK\r\n" {
		t.Fatalf("initial SET=%q", got)
	}

	srv.SetJournal(failingSetJournal{})
	if _, err := srv.Execute([][]byte{
		[]byte("SET"),
		[]byte("key"),
		[]byte("new"),
	}); err == nil {
		t.Fatal("expected persistence failure")
	}

	if got := execute(t, srv, "GET", "key"); got != "$3\r\nold\r\n" {
		t.Fatalf("GET after failed append=%q", got)
	}
}


func TestConcurrentPlainSetAOFReplayMatchesLiveValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.snug")
	store := engine.New()
	journal, err := persistence.Open(path, "no")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(store)
	srv.SetJournal(journal)

	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 250; i++ {
				value := []byte(fmt.Sprintf("%d:%d", worker, i))
				if _, _, handled, err := srv.executeAuthorizedConcurrentAOFSet(
					[][]byte{[]byte("SET"), []byte("same"), value},
				); !handled || err != nil {
					t.Errorf("worker=%d i=%d handled=%v err=%v", worker, i, handled, err)
					return
				}
			}
		}()
	}
	wg.Wait()

	live, found, wrongType := store.GetString("same")
	if wrongType || !found {
		t.Fatalf("live value found=%v wrongType=%v", found, wrongType)
	}
	live = append([]byte(nil), live...)

	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := engine.New()
	if err := persistence.Replay(path, func(records []persistence.Record) error {
		return restarted.Restore(records, false)
	}); err != nil {
		t.Fatal(err)
	}
	replayed, found, wrongType := restarted.GetString("same")
	if wrongType || !found {
		t.Fatalf("replayed value found=%v wrongType=%v", found, wrongType)
	}
	if string(replayed) != string(live) {
		t.Fatalf("replayed=%q live=%q", replayed, live)
	}
}
