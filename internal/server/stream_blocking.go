package server

import (
	"errors"
	"sync"
	"time"
)

type streamWaiter struct {
	ch   chan struct{}
	once sync.Once
	keys []string
}

type streamWaitRegistry struct {
	mu      sync.Mutex
	byKey   map[string]map[*streamWaiter]struct{}
	stopped bool
}

var streamWaitRegistries sync.Map // map[*Server]*streamWaitRegistry

func streamRegistryForServer(s *Server) *streamWaitRegistry {
	if existing, ok := streamWaitRegistries.Load(s); ok {
		return existing.(*streamWaitRegistry)
	}
	created := &streamWaitRegistry{byKey: make(map[string]map[*streamWaiter]struct{})}
	actual, _ := streamWaitRegistries.LoadOrStore(s, created)
	return actual.(*streamWaitRegistry)
}

func (s *Server) registerStreamWaiter(keys []string) (*streamWaiter, bool) {
	registry := streamRegistryForServer(s)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.stopped {
		return nil, false
	}
	waiter := &streamWaiter{ch: make(chan struct{})}
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		waiter.keys = append(waiter.keys, key)
		set := registry.byKey[key]
		if set == nil {
			set = make(map[*streamWaiter]struct{})
			registry.byKey[key] = set
		}
		set[waiter] = struct{}{}
	}
	return waiter, true
}

func (s *Server) unregisterStreamWaiter(waiter *streamWaiter) {
	if waiter == nil {
		return
	}
	registry := streamRegistryForServer(s)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for _, key := range waiter.keys {
		set := registry.byKey[key]
		delete(set, waiter)
		if len(set) == 0 {
			delete(registry.byKey, key)
		}
	}
}

func (s *Server) signalStreamKey(key string) {
	registry := streamRegistryForServer(s)
	registry.mu.Lock()
	waiters := registry.byKey[key]
	delete(registry.byKey, key)
	for waiter := range waiters {
		waiter.once.Do(func() { close(waiter.ch) })
	}
	registry.mu.Unlock()
}

func (s *Server) streamBlockingStopped() bool {
	registry := streamRegistryForServer(s)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return registry.stopped
}

func (s *Server) CancelBlockingStreams() {
	registry := streamRegistryForServer(s)
	registry.mu.Lock()
	if registry.stopped {
		registry.mu.Unlock()
		return
	}
	registry.stopped = true
	unique := make(map[*streamWaiter]struct{})
	for _, set := range registry.byKey {
		for waiter := range set {
			unique[waiter] = struct{}{}
		}
	}
	registry.byKey = make(map[string]map[*streamWaiter]struct{})
	for waiter := range unique {
		waiter.once.Do(func() { close(waiter.ch) })
	}
	registry.mu.Unlock()
}

func (s *Server) executeBlockingStream(args [][]byte, cancel <-chan struct{}) ([]byte, error) {
	request, err := parseXRead(args)
	if err != nil {
		return nil, err
	}
	if !request.hasBlock {
		return s.executeXRead(args)
	}
	resolved, err := s.store.ResolveStreamReadCursors(request.keys, request.cursors)
	if err != nil {
		return nil, err
	}
	timer, timeoutC := blockingTimer(request.block)
	if timer != nil {
		defer timer.Stop()
	}
	for {
		waiter, ok := s.registerStreamWaiter(request.keys)
		if !ok {
			return nil, errBlockingCanceled
		}
		results, err := s.store.StreamReadAfter(request.keys, resolved, request.count)
		if err != nil {
			s.unregisterStreamWaiter(waiter)
			return nil, err
		}
		if len(results) > 0 {
			s.unregisterStreamWaiter(waiter)
			return streamReadResponse(results), nil
		}
		select {
		case <-waiter.ch:
			s.unregisterStreamWaiter(waiter)
			if s.streamBlockingStopped() {
				return nil, errBlockingCanceled
			}
		case <-timeoutC:
			s.unregisterStreamWaiter(waiter)
			return []byte("*-1\r\n"), nil
		case <-cancel:
			s.unregisterStreamWaiter(waiter)
			return nil, errBlockingClientGone
		}
	}
}

func (s *Server) signalStreamAvailability(args [][]byte, response []byte) {
	if len(args) < 2 || len(response) == 0 {
		return
	}
	if string(args[0]) != "XADD" && string(args[0]) != "xadd" {
		return
	}
	if string(response) == "$-1\r\n" {
		return
	}
	s.signalStreamKey(string(args[1]))
}

var _ = errors.Is
var _ = time.Second
