package server

import (
	"snugkv/internal/engine"
	"sync/atomic"
	"testing"
)

func TestAuthorizedConcurrentSetFastPath(t *testing.T) {
	store := engine.New()
	s := New(store)

	got, handled, err := s.executeAuthorizedConcurrentSet([][]byte{
		[]byte("SET"),
		[]byte("key"),
		[]byte("value"),
	})
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	if string(got) != "+OK\r\n" {
		t.Fatalf("SET = %q", got)
	}

	value, ok := store.Get("key")
	if !ok || string(value) != "value" {
		t.Fatalf("stored value=%q ok=%t", value, ok)
	}

	if got := atomic.LoadUint64(&s.commands); got != 1 {
		t.Fatalf("commands=%d want=1", got)
	}

	got, handled, err = s.executeAuthorizedConcurrentSet([][]byte{
		[]byte("SET"),
		[]byte("key"),
		[]byte("next"),
	})
	if err != nil || !handled {
		t.Fatalf("overwrite handled=%t err=%v", handled, err)
	}
	value, ok = store.Get("key")
	if !ok || string(value) != "next" {
		t.Fatalf("overwrite value=%q ok=%t", value, ok)
	}
}

func TestAuthorizedConcurrentSetFastPathFallbacks(t *testing.T) {
	t.Run("non-plain-set", func(t *testing.T) {
		s := New(engine.New())
		if _, handled, err := s.executeAuthorizedConcurrentSet([][]byte{
			[]byte("SET"),
			[]byte("key"),
			[]byte("value"),
			[]byte("NX"),
		}); err != nil || handled {
			t.Fatalf("handled=%t err=%v", handled, err)
		}
	})

	t.Run("aof", func(t *testing.T) {
		s := New(engine.New())
		s.SetJournal(getFastPathJournal{})
		if _, handled, err := s.executeAuthorizedConcurrentSet([][]byte{
			[]byte("SET"),
			[]byte("key"),
			[]byte("value"),
		}); err != nil || handled {
			t.Fatalf("handled=%t err=%v", handled, err)
		}
	})

	t.Run("metrics", func(t *testing.T) {
		s := New(engine.New())
		atomic.StoreUint32(&s.metricsEnabled, 1)
		if _, handled, err := s.executeAuthorizedConcurrentSet([][]byte{
			[]byte("SET"),
			[]byte("key"),
			[]byte("value"),
		}); err != nil || handled {
			t.Fatalf("handled=%t err=%v", handled, err)
		}
	})

	t.Run("watch", func(t *testing.T) {
		s := New(engine.New())
		tx := newTransactionSession(s)
		defer tx.close()

		if _, err := tx.watch([][]byte{[]byte("watched")}); err != nil {
			t.Fatal(err)
		}

		if _, handled, err := s.executeAuthorizedConcurrentSet([][]byte{
			[]byte("SET"),
			[]byte("watched"),
			[]byte("value"),
		}); err != nil || handled {
			t.Fatalf("handled=%t err=%v", handled, err)
		}
	})

	t.Run("maxmemory", func(t *testing.T) {
		store := engine.New()
		store.SetMaxMemory(1024)
		s := New(store)

		if _, handled, err := s.executeAuthorizedConcurrentSet([][]byte{
			[]byte("SET"),
			[]byte("key"),
			[]byte("value"),
		}); err != nil || handled {
			t.Fatalf("handled=%t err=%v", handled, err)
		}
	})
}
