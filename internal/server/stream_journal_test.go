package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

func streamWorkload(t *testing.T, srv *Server, n int) {
	t.Helper()
	execute(t, srv, "XGROUP", "CREATE", "jobs", "workers", "$", "MKSTREAM")
	payload := strings.Repeat("p", 100)
	for i := 1; i <= n; i++ {
		execute(t, srv, "XADD", "jobs", fmt.Sprintf("%d-0", i), "k", payload)
		if i%10 == 0 {
			execute(t, srv, "XREADGROUP", "GROUP", "workers", "c1", "COUNT", "10", "STREAMS", "jobs", ">")
		}
		if i%10 == 0 {
			ids := []string{"XACK", "jobs", "workers"}
			for k := i - 9; k <= i; k++ {
				ids = append(ids, fmt.Sprintf("%d-0", k))
			}
			execute(t, srv, ids...)
		}
		if i%20 == 0 {
			execute(t, srv, "XDEL", "jobs", fmt.Sprintf("%d-0", i-9))
		}
		if i%50 == 0 {
			execute(t, srv, "XTRIM", "jobs", "MAXLEN", "~", "300")
		}
	}
}

func TestStreamAOFIsSmallAndRestartsIdentically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.snug")
	first, _ := engine.NewWithOptions(engine.Options{Shards: 16})
	journal, err := persistence.Open(path, "always")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(first)
	srv.SetJournal(journal)
	streamWorkload(t, srv, 2000)
	want := first.Export([]string{"jobs"})
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Size() > 2<<20 {
		t.Fatalf("AOF is %d bytes for 2000 small messages", info.Size())
	}
	restarted, _ := engine.NewWithOptions(engine.Options{Shards: 16})
	if err := persistence.Replay(path, func(r []persistence.Record) error { return restarted.Restore(r, false) }); err != nil {
		t.Fatal(err)
	}
	if got := restarted.Export([]string{"jobs"}); !reflect.DeepEqual(got, want) {
		t.Fatal("restarted stream differs")
	}
}

type toggleJournal struct{ fail bool }

func (f *toggleJournal) Append([]persistence.Record) error {
	if f.fail {
		return errors.New("disk full")
	}
	return nil
}

func TestStreamAppendFailureRollsBack(t *testing.T) {
	store, _ := engine.NewWithOptions(engine.Options{Shards: 16})
	j := &toggleJournal{}
	srv := New(store)
	srv.SetJournal(j)
	execute(t, srv, "XADD", "s", "1-0", "a", "b")
	execute(t, srv, "XGROUP", "CREATE", "s", "g", "0")
	before := store.Export([]string{"s"})
	j.fail = true
	srv.durabilityFailed = false
	if _, err := srv.Execute([][]byte{[]byte("XADD"), []byte("s"), []byte("2-0"), []byte("c"), []byte("d")}); err == nil {
		t.Fatal("expected append failure")
	}
	if after := store.Export([]string{"s"}); !reflect.DeepEqual(before, after) {
		t.Fatal("failed XADD was not rolled back")
	}
	srv.durabilityFailed = false
	if _, err := srv.Execute([][]byte{[]byte("XREADGROUP"), []byte("GROUP"), []byte("g"), []byte("c"), []byte("STREAMS"), []byte("s"), []byte(">")}); err == nil {
		t.Fatal("expected append failure")
	}
	if after := store.Export([]string{"s"}); !reflect.DeepEqual(before, after) {
		t.Fatal("failed XREADGROUP was not rolled back")
	}
}
