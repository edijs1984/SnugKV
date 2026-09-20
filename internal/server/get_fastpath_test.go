package server

import (
	"errors"
	"snugkv/internal/engine"
	"snugkv/internal/persistence"
	"sync/atomic"
	"testing"
)

type getFastPathJournal struct{}

func (getFastPathJournal) Append([]persistence.Record) error { return nil }

func TestAuthorizedConcurrentGetFastPath(t *testing.T) {
	store := engine.New()
	s := New(store)

	if err := store.Set("key", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}

	got, handled, err := s.executeAuthorizedConcurrentGet([][]byte{
		[]byte("GET"),
		[]byte("key"),
	})
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	if string(got) != "$5\r\nvalue\r\n" {
		t.Fatalf("GET = %q", got)
	}

	got, handled, err = s.executeAuthorizedConcurrentGet([][]byte{
		[]byte("GET"),
		[]byte("missing"),
	})
	if err != nil || !handled {
		t.Fatalf("missing handled=%t err=%v", handled, err)
	}
	if string(got) != "$-1\r\n" {
		t.Fatalf("missing GET = %q", got)
	}

	if got := atomic.LoadUint64(&s.commands); got != 2 {
		t.Fatalf("commands=%d want=2", got)
	}
}

func TestAuthorizedConcurrentGetFastPathWrongType(t *testing.T) {
	store := engine.New()
	s := New(store)

	if _, _, err := store.StreamAdd(
		"events",
		"1-0",
		[]engine.StreamField{{Field: []byte("field"), Value: []byte("value")}},
		engine.StreamAddOptions{},
	); err != nil {
		t.Fatal(err)
	}

	_, handled, err := s.executeAuthorizedConcurrentGet([][]byte{
		[]byte("GET"),
		[]byte("events"),
	})
	if !handled {
		t.Fatal("stream GET did not use eligible fast path")
	}
	if !errors.Is(err, errWrongType) {
		t.Fatalf("err=%v want WRONGTYPE", err)
	}
}

func TestAuthorizedConcurrentGetFastPathFallbacks(t *testing.T) {
	t.Run("non-get", func(t *testing.T) {
		s := New(engine.New())
		if _, handled, err := s.executeAuthorizedConcurrentGet([][]byte{
			[]byte("PING"),
		}); err != nil || handled {
			t.Fatalf("handled=%t err=%v", handled, err)
		}
	})

	t.Run("aof", func(t *testing.T) {
		s := New(engine.New())
		s.SetJournal(getFastPathJournal{})
		if _, handled, err := s.executeAuthorizedConcurrentGet([][]byte{
			[]byte("GET"),
			[]byte("key"),
		}); err != nil || handled {
			t.Fatalf("handled=%t err=%v", handled, err)
		}
	})

	t.Run("metrics", func(t *testing.T) {
		s := New(engine.New())
		atomic.StoreUint32(&s.metricsEnabled, 1)
		if _, handled, err := s.executeAuthorizedConcurrentGet([][]byte{
			[]byte("GET"),
			[]byte("key"),
		}); err != nil || handled {
			t.Fatalf("handled=%t err=%v", handled, err)
		}
	})

	t.Run("watch", func(t *testing.T) {
		store := engine.New()
		s := New(store)
		tx := newTransactionSession(s)
		defer tx.close()

		if _, err := tx.watch([][]byte{[]byte("watched")}); err != nil {
			t.Fatal(err)
		}

		if _, handled, err := s.executeAuthorizedConcurrentGet([][]byte{
			[]byte("GET"),
			[]byte("watched"),
		}); err != nil || handled {
			t.Fatalf("handled=%t err=%v", handled, err)
		}
	})
}


func TestAuthorizedConcurrentRawGetFastPath(t *testing.T) {
	store := engine.New()
	s := New(store)

	if err := store.Set("key", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}

	var payload []byte
	handled, err := s.executeAuthorizedConcurrentRawGet(
		[][]byte{[]byte("GET"), []byte("key")},
		func(value []byte) error {
			payload = append(payload, value...)
			return nil
		},
	)
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	if string(payload) != "value" {
		t.Fatalf("payload=%q", payload)
	}
}

func TestAuthorizedConcurrentRawGetFallsBackWhenEncodingEnabled(t *testing.T) {
	store, err := engine.NewWithOptions(engine.Options{
		Shards:   256,
		Encoding: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := New(store)

	if err := store.Set("key", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}

	handled, err := s.executeAuthorizedConcurrentRawGet(
		[][]byte{[]byte("GET"), []byte("key")},
		func([]byte) error {
			t.Fatal("encoded store unexpectedly used raw visitor")
			return nil
		},
	)
	if err != nil || handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
}
