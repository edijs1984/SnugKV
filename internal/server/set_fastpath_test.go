package server

import (
	"fmt"
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


func TestAuthorizedConcurrentSetBatchChunksPreserveOrderAndFallback(t *testing.T) {
	store := engine.New()
	s := New(store)

	const n = 70
	keys := make([][]byte, n)
	values := make([][]byte, n)
	for i := 0; i < n; i++ {
		keys[i] = []byte(fmt.Sprintf("batch:%03d", i))
		values[i] = []byte(fmt.Sprintf("value:%03d", i))
	}

	// Force the middle chunk to use the ordinary SET fallback.
	if err := store.SetPlain(string(keys[40]), []byte("old")); err != nil {
		t.Fatal(err)
	}

	handled, err := s.executeAuthorizedConcurrentSetBatch(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("batch fast path did not handle request")
	}

	for i := 0; i < n; i++ {
		got, found, wrongType := store.GetString(string(keys[i]))
		if wrongType || !found || string(got) != string(values[i]) {
			t.Fatalf("key %q got=%q found=%t wrongType=%t", keys[i], got, found, wrongType)
		}
	}

	if got := atomic.LoadUint64(&s.commands); got != n {
		t.Fatalf("commands=%d want=%d", got, n)
	}
}
