package server

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func TestAutoSetBatcherSingleRequest(t *testing.T) {
	store := engine.New()
	server := New(store)
	batcher := newAutoSetBatcher(server, 64, 10*time.Microsecond)
	defer batcher.close()

	handled, err := batcher.submit([]byte("auto:single"), []byte("value"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !handled {
		t.Fatal("single SET was not handled")
	}
	got, ok := store.Get("auto:single")
	if !ok || string(got) != "value" {
		t.Fatalf("GET=(%q,%v), want value,true", got, ok)
	}
}

func TestAutoSetBatcherCoalescesConcurrentRequestsCorrectly(t *testing.T) {
	store := engine.New()
	server := New(store)
	batcher := newAutoSetBatcher(server, 64, 500*time.Microsecond)
	defer batcher.close()

	const requests = 32
	var wg sync.WaitGroup
	errs := make(chan error, requests)
	start := make(chan struct{})

	for i := 0; i < requests; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			key := []byte(fmt.Sprintf("auto:%02d", i))
			value := []byte(fmt.Sprintf("value:%02d", i))
			handled, err := batcher.submit(key, value)
			if err != nil {
				errs <- err
				return
			}
			if !handled {
				errs <- fmt.Errorf("request %d was not handled", i)
			}
		}()
	}

	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	for i := 0; i < requests; i++ {
		key := fmt.Sprintf("auto:%02d", i)
		want := fmt.Sprintf("value:%02d", i)
		got, ok := store.Get(key)
		if !ok || string(got) != want {
			t.Fatalf("GET %q=(%q,%v), want %q,true", key, got, ok, want)
		}
	}
}

func TestAutoSetBatcherCloseUnblocksSubmitters(t *testing.T) {
	store := engine.New()
	server := New(store)
	batcher := newAutoSetBatcher(server, 64, time.Second)

	done := make(chan struct{})
	go func() {
		_, _ = batcher.submit([]byte("auto:close"), []byte("value"))
		close(done)
	}()

	time.Sleep(10 * time.Millisecond)
	batcher.close()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("submitter remained blocked after batcher close")
	}
}
