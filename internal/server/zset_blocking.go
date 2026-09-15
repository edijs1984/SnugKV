package server

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

var zsetBlockingCommands = map[string]commandInfo{
	"BZPOPMIN": {3, 0, 1, -2, 1, true},
	"BZPOPMAX": {3, 0, 1, -2, 1, true},
	"BZMPOP":   {5, 0, 0, 0, 0, true},
}

func init() {
	for name, info := range zsetBlockingCommands {
		commandTable[name] = info
	}
}

type zsetWaiter struct {
	ch   chan struct{}
	once sync.Once
	keys []string
}

type zsetWaitRegistry struct {
	mu      sync.Mutex
	byKey   map[string]map[*zsetWaiter]struct{}
	stopped bool
}

var zsetWaitRegistries sync.Map // map[*Server]*zsetWaitRegistry

func zsetRegistryForServer(s *Server) *zsetWaitRegistry {
	if existing, ok := zsetWaitRegistries.Load(s); ok {
		return existing.(*zsetWaitRegistry)
	}
	created := &zsetWaitRegistry{byKey: make(map[string]map[*zsetWaiter]struct{})}
	actual, _ := zsetWaitRegistries.LoadOrStore(s, created)
	return actual.(*zsetWaitRegistry)
}

func (s *Server) registerZSetWaiter(keys []string) (*zsetWaiter, bool) {
	registry := zsetRegistryForServer(s)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.stopped {
		return nil, false
	}

	waiter := &zsetWaiter{ch: make(chan struct{})}
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		waiter.keys = append(waiter.keys, key)
		set := registry.byKey[key]
		if set == nil {
			set = make(map[*zsetWaiter]struct{})
			registry.byKey[key] = set
		}
		set[waiter] = struct{}{}
	}
	return waiter, true
}

func (s *Server) unregisterZSetWaiter(waiter *zsetWaiter) {
	if waiter == nil {
		return
	}
	registry := zsetRegistryForServer(s)
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

func (s *Server) signalZSetKey(key string) {
	registry := zsetRegistryForServer(s)
	registry.mu.Lock()
	waiters := registry.byKey[key]
	delete(registry.byKey, key)
	for waiter := range waiters {
		waiter.once.Do(func() { close(waiter.ch) })
	}
	registry.mu.Unlock()
}

func (s *Server) zsetBlockingStopped() bool {
	registry := zsetRegistryForServer(s)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return registry.stopped
}

func (s *Server) CancelBlockingZSets() {
	registry := zsetRegistryForServer(s)
	registry.mu.Lock()
	if registry.stopped {
		registry.mu.Unlock()
		return
	}
	registry.stopped = true
	unique := make(map[*zsetWaiter]struct{})
	for _, set := range registry.byKey {
		for waiter := range set {
			unique[waiter] = struct{}{}
		}
	}
	registry.byKey = make(map[string]map[*zsetWaiter]struct{})
	for waiter := range unique {
		waiter.once.Do(func() { close(waiter.ch) })
	}
	registry.mu.Unlock()
}

func isBlockingZSetCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := zsetBlockingCommands[strings.ToUpper(string(args[0]))]
	return ok
}

func (s *Server) executeBlockingZSet(args [][]byte) ([]byte, error) {
	cmd := strings.ToUpper(string(args[0]))
	info, ok := zsetBlockingCommands[cmd]
	if !ok || len(args) < info.min || info.max > 0 && len(args) > info.max {
		return nil, fmt.Errorf("ERR wrong number of arguments for '%s' command", strings.ToLower(cmd))
	}

	switch cmd {
	case "BZPOPMIN", "BZPOPMAX":
		timeout, err := parseBlockingTimeout(args[len(args)-1])
		if err != nil {
			return nil, err
		}
		keys := make([]string, 0, len(args)-2)
		for _, arg := range args[1 : len(args)-1] {
			keys = append(keys, string(arg))
		}
		return s.blockingZPop(keys, cmd == "BZPOPMAX", timeout)

	case "BZMPOP":
		timeout, err := parseBlockingTimeout(args[1])
		if err != nil {
			return nil, err
		}
		numKeys, err := strconv.Atoi(string(args[2]))
		if err != nil || numKeys <= 0 {
			return nil, errors.New("ERR numkeys should be greater than 0")
		}
		sideIndex := 3 + numKeys
		if sideIndex >= len(args) {
			return nil, errors.New("ERR syntax error")
		}
		keys := make([]string, numKeys)
		for i := 0; i < numKeys; i++ {
			keys[i] = string(args[3+i])
		}
		side := strings.ToUpper(string(args[sideIndex]))
		if side != "MIN" && side != "MAX" {
			return nil, errors.New("ERR syntax error")
		}
		if sideIndex+1 < len(args) {
			if sideIndex+3 != len(args) || !strings.EqualFold(string(args[sideIndex+1]), "COUNT") {
				return nil, errors.New("ERR syntax error")
			}
			count, err := strconv.ParseInt(string(args[sideIndex+2]), 10, 64)
			if err != nil || count <= 0 {
				return nil, errors.New("ERR count should be greater than 0")
			}
		}
		return s.blockingZMPop(args, keys, timeout)
	}

	return nil, errors.New("ERR unknown blocking zset command")
}

func (s *Server) waitForZSetSignal(waiter *zsetWaiter, timeoutC <-chan time.Time) error {
	select {
	case <-waiter.ch:
		s.unregisterZSetWaiter(waiter)
		if s.zsetBlockingStopped() {
			return errBlockingCanceled
		}
		return nil
	case <-timeoutC:
		s.unregisterZSetWaiter(waiter)
		return errBlockingTimeout
	}
}

var errBlockingTimeout = errors.New("blocking timeout")

func (s *Server) blockingZPop(keys []string, max bool, timeout time.Duration) ([]byte, error) {
	timer, timeoutC := blockingTimer(timeout)
	if timer != nil {
		defer timer.Stop()
	}

	for {
		waiter, ok := s.registerZSetWaiter(keys)
		if !ok {
			return nil, errBlockingCanceled
		}

		for _, key := range keys {
			cardinality, err := s.store.ZSetCard(key)
			if err != nil {
				s.unregisterZSetWaiter(waiter)
				return nil, err
			}
			if cardinality == 0 {
				continue
			}

			op := "ZPOPMIN"
			if max {
				op = "ZPOPMAX"
			}
			response, err := s.executeDurable([][]byte{[]byte(op), []byte(key)})
			if err != nil {
				s.unregisterZSetWaiter(waiter)
				return nil, err
			}
			if bytes.Equal(response, []byte("*0\r\n")) {
				continue
			}
			const pairPrefix = "*2\r\n"
			if !bytes.HasPrefix(response, []byte(pairPrefix)) {
				s.unregisterZSetWaiter(waiter)
				return nil, errors.New("ERR internal zset pop response")
			}
			out := []byte("*3\r\n")
			out = append(out, formatBulkString([]byte(key))...)
			out = append(out, response[len(pairPrefix):]...)
			s.unregisterZSetWaiter(waiter)
			return out, nil
		}

		if err := s.waitForZSetSignal(waiter, timeoutC); err != nil {
			if errors.Is(err, errBlockingTimeout) {
				return []byte("*-1\r\n"), nil
			}
			return nil, err
		}
	}
}

func (s *Server) blockingZMPop(original [][]byte, keys []string, timeout time.Duration) ([]byte, error) {
	timer, timeoutC := blockingTimer(timeout)
	if timer != nil {
		defer timer.Stop()
	}

	command := make([][]byte, 0, len(original)-1)
	command = append(command, []byte("ZMPOP"))
	command = append(command, original[2:]...)

	for {
		waiter, ok := s.registerZSetWaiter(keys)
		if !ok {
			return nil, errBlockingCanceled
		}

		ready := false
		for _, key := range keys {
			cardinality, err := s.store.ZSetCard(key)
			if err != nil {
				s.unregisterZSetWaiter(waiter)
				return nil, err
			}
			if cardinality > 0 {
				ready = true
				break
			}
		}
		if ready {
			response, err := s.executeDurable(command)
			if err != nil {
				s.unregisterZSetWaiter(waiter)
				return nil, err
			}
			if !bytes.Equal(response, []byte("*-1\r\n")) {
				s.unregisterZSetWaiter(waiter)
				return response, nil
			}
		}

		if err := s.waitForZSetSignal(waiter, timeoutC); err != nil {
			if errors.Is(err, errBlockingTimeout) {
				return []byte("*-1\r\n"), nil
			}
			return nil, err
		}
	}
}
