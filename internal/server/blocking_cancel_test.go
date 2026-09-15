package server

import (
	"errors"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func waitForListRegistration(t *testing.T, s *Server, key string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		registry := registryForServer(s)
		registry.mu.Lock()
		count := len(registry.byKey[key])
		registry.mu.Unlock()
		if count > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("list waiter for %q was not registered", key)
}

func waitForZSetRegistration(t *testing.T, s *Server, key string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		registry := zsetRegistryForServer(s)
		registry.mu.Lock()
		count := len(registry.byKey[key])
		registry.mu.Unlock()
		if count > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("zset waiter for %q was not registered", key)
}

func assertNoListRegistration(t *testing.T, s *Server, key string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		registry := registryForServer(s)
		registry.mu.Lock()
		count := len(registry.byKey[key])
		registry.mu.Unlock()
		if count == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("list waiter for %q remained registered", key)
}

func assertNoZSetRegistration(t *testing.T, s *Server, key string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		registry := zsetRegistryForServer(s)
		registry.mu.Lock()
		count := len(registry.byKey[key])
		registry.mu.Unlock()
		if count == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("zset waiter for %q remained registered", key)
}

func TestExecuteWithCancelReleasesListWaiter(t *testing.T) {
	s := New(engine.New())
	cancel := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		_, err := s.ExecuteWithCancel([][]byte{[]byte("BLPOP"), []byte("list:cancel"), []byte("0")}, cancel)
		done <- err
	}()

	waitForListRegistration(t, s, "list:cancel")
	close(cancel)

	select {
	case err := <-done:
		if !errors.Is(err, errBlockingClientGone) {
			t.Fatalf("BLPOP cancel error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BLPOP did not return after cancellation")
	}
	assertNoListRegistration(t, s, "list:cancel")
}

func TestExecuteWithCancelReleasesZSetWaiter(t *testing.T) {
	s := New(engine.New())
	cancel := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		_, err := s.ExecuteWithCancel([][]byte{[]byte("BZPOPMIN"), []byte("zset:cancel"), []byte("0")}, cancel)
		done <- err
	}()

	waitForZSetRegistration(t, s, "zset:cancel")
	close(cancel)

	select {
	case err := <-done:
		if !errors.Is(err, errBlockingClientGone) {
			t.Fatalf("BZPOPMIN cancel error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BZPOPMIN did not return after cancellation")
	}
	assertNoZSetRegistration(t, s, "zset:cancel")
}
