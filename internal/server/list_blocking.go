package server

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

var errBlockingCanceled = errors.New("ERR server is shutting down")

type listWaiter struct {
	ch   chan struct{}
	once sync.Once
	keys []string
}

type listWaitRegistry struct {
	mu      sync.Mutex
	byKey   map[string]map[*listWaiter]struct{}
	stopped bool
}

var listWaitRegistries sync.Map // map[*Server]*listWaitRegistry

func registryForServer(s *Server) *listWaitRegistry {
	if existing, ok := listWaitRegistries.Load(s); ok {
		return existing.(*listWaitRegistry)
	}
	created := &listWaitRegistry{byKey: make(map[string]map[*listWaiter]struct{})}
	actual, _ := listWaitRegistries.LoadOrStore(s, created)
	return actual.(*listWaitRegistry)
}

func (s *Server) registerListWaiter(keys []string) (*listWaiter, bool) {
	registry := registryForServer(s)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.stopped {
		return nil, false
	}

	waiter := &listWaiter{ch: make(chan struct{})}
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		waiter.keys = append(waiter.keys, key)
		set := registry.byKey[key]
		if set == nil {
			set = make(map[*listWaiter]struct{})
			registry.byKey[key] = set
		}
		set[waiter] = struct{}{}
	}
	return waiter, true
}

func (s *Server) unregisterListWaiter(waiter *listWaiter) {
	if waiter == nil {
		return
	}
	registry := registryForServer(s)
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

func (s *Server) signalListKey(key string) {
	registry := registryForServer(s)
	registry.mu.Lock()
	waiters := registry.byKey[key]
	delete(registry.byKey, key)
	for waiter := range waiters {
		waiter.once.Do(func() { close(waiter.ch) })
	}
	registry.mu.Unlock()
}

func (s *Server) blockingStopped() bool {
	registry := registryForServer(s)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return registry.stopped
}

// CancelBlocking releases all blocking LIST commands. It is idempotent and is
// used by the TCP server during shutdown so an infinite timeout cannot retain a
// connection goroutine forever.
func (s *Server) CancelBlocking() {
	registry := registryForServer(s)
	registry.mu.Lock()
	if registry.stopped {
		registry.mu.Unlock()
		return
	}
	registry.stopped = true
	unique := make(map[*listWaiter]struct{})
	for _, set := range registry.byKey {
		for waiter := range set {
			unique[waiter] = struct{}{}
		}
	}
	registry.byKey = make(map[string]map[*listWaiter]struct{})
	for waiter := range unique {
		waiter.once.Do(func() { close(waiter.ch) })
	}
	registry.mu.Unlock()
}

func isBlockingListCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	switch strings.ToUpper(string(args[0])) {
	case "BLPOP", "BRPOP", "BLMOVE", "BRPOPLPUSH":
		return true
	default:
		return false
	}
}

func parseBlockingTimeout(arg []byte) (time.Duration, error) {
	seconds, err := strconv.ParseFloat(string(arg), 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		return 0, errors.New("ERR timeout is not a float or out of range")
	}
	if seconds == 0 {
		return 0, nil
	}
	nanos := seconds * float64(time.Second)
	if nanos > float64(math.MaxInt64) {
		return 0, errors.New("ERR timeout is not a float or out of range")
	}
	return time.Duration(nanos), nil
}

func (s *Server) executeBlockingList(args [][]byte) ([]byte, error) {
	cmd := strings.ToUpper(string(args[0]))
	info, ok := listCommands[cmd]
	if !ok || len(args) < info.min || info.max > 0 && len(args) > info.max {
		return nil, fmt.Errorf("ERR wrong number of arguments for '%s' command", strings.ToLower(cmd))
	}

	switch cmd {
	case "BLPOP", "BRPOP":
		timeout, err := parseBlockingTimeout(args[len(args)-1])
		if err != nil {
			return nil, err
		}
		keys := make([]string, 0, len(args)-2)
		for _, arg := range args[1 : len(args)-1] {
			keys = append(keys, string(arg))
		}
		return s.blockingPop(keys, cmd == "BLPOP", timeout)

	case "BLMOVE":
		timeout, err := parseBlockingTimeout(args[5])
		if err != nil {
			return nil, err
		}
		sourceSide := strings.ToUpper(string(args[3]))
		destinationSide := strings.ToUpper(string(args[4]))
		if (sourceSide != "LEFT" && sourceSide != "RIGHT") || (destinationSide != "LEFT" && destinationSide != "RIGHT") {
			return nil, errors.New("ERR syntax error")
		}
		return s.blockingMove(string(args[1]), string(args[2]), sourceSide, destinationSide, timeout, false)

	case "BRPOPLPUSH":
		timeout, err := parseBlockingTimeout(args[3])
		if err != nil {
			return nil, err
		}
		return s.blockingMove(string(args[1]), string(args[2]), "RIGHT", "LEFT", timeout, true)
	}

	return nil, errors.New("ERR unknown blocking list command")
}

func blockingTimer(timeout time.Duration) (*time.Timer, <-chan time.Time) {
	if timeout <= 0 {
		return nil, nil
	}
	timer := time.NewTimer(timeout)
	return timer, timer.C
}

func (s *Server) blockingPop(keys []string, left bool, timeout time.Duration) ([]byte, error) {
	timer, timeoutC := blockingTimer(timeout)
	if timer != nil {
		defer timer.Stop()
	}

	for {
		waiter, ok := s.registerListWaiter(keys)
		if !ok {
			return nil, errBlockingCanceled
		}

		for _, key := range keys {
			length, err := s.store.ListLen(key)
			if err != nil {
				s.unregisterListWaiter(waiter)
				return nil, err
			}
			if length == 0 {
				continue
			}

			op := "RPOP"
			if left {
				op = "LPOP"
			}
			response, err := s.executeDurable([][]byte{[]byte(op), []byte(key)})
			if err != nil {
				s.unregisterListWaiter(waiter)
				return nil, err
			}
			if string(response) != "$-1\r\n" {
				s.unregisterListWaiter(waiter)
				return array(formatBulkString([]byte(key)), response), nil
			}
		}

		select {
		case <-waiter.ch:
			s.unregisterListWaiter(waiter)
			if s.blockingStopped() {
				return nil, errBlockingCanceled
			}
		case <-timeoutC:
			s.unregisterListWaiter(waiter)
			return []byte("*-1\r\n"), nil
		}
	}
}

func (s *Server) blockingMove(source, destination, sourceSide, destinationSide string, timeout time.Duration, legacy bool) ([]byte, error) {
	timer, timeoutC := blockingTimer(timeout)
	if timer != nil {
		defer timer.Stop()
	}

	for {
		waiter, ok := s.registerListWaiter([]string{source})
		if !ok {
			return nil, errBlockingCanceled
		}

		length, err := s.store.ListLen(source)
		if err != nil {
			s.unregisterListWaiter(waiter)
			return nil, err
		}
		if length > 0 {
			var command [][]byte
			if legacy {
				command = [][]byte{[]byte("RPOPLPUSH"), []byte(source), []byte(destination)}
			} else {
				command = [][]byte{[]byte("LMOVE"), []byte(source), []byte(destination), []byte(sourceSide), []byte(destinationSide)}
			}
			response, err := s.executeDurable(command)
			if err != nil {
				s.unregisterListWaiter(waiter)
				return nil, err
			}
			if string(response) != "$-1\r\n" {
				s.unregisterListWaiter(waiter)
				return response, nil
			}
		}

		select {
		case <-waiter.ch:
			s.unregisterListWaiter(waiter)
			if s.blockingStopped() {
				return nil, errBlockingCanceled
			}
		case <-timeoutC:
			s.unregisterListWaiter(waiter)
			return nullBulk(), nil
		}
	}
}
