package server

import (
	"bytes"
	"errors"
	"snugkv/internal/persistence"
	"strings"
	"sync/atomic"
	"time"
)

type Journal interface {
	Append([]persistence.Record) error
}

// SetJournal is a startup-only operation. Command execution is serialized so
// clients cannot observe a mutation whose journal append later fails, and so a
// MULTI/EXEC block can execute without another client interleaving commands.
func (s *Server) SetJournal(j Journal) { s.journal = j }

func (s *Server) Execute(args [][]byte) ([]byte, error) {
	return s.executeWithCancelSession(args, nil, nil)
}

func (s *Server) executeForSession(
	args [][]byte,
	session *authSession,
) ([]byte, error) {
	return s.executeWithCancelSession(args, nil, session)
}

// ExecuteWithCancel is identical to Execute except that blocking commands also
// stop when cancel is closed. Non-blocking commands intentionally ignore cancel.
// The TCP server uses this to release per-connection waiters when a peer goes
// away without changing the behavior of direct/in-process callers.
func (s *Server) ExecuteWithCancel(args [][]byte, cancel <-chan struct{}) (response []byte, resultErr error) {
	return s.executeWithCancelSession(args, cancel, nil)
}

func (s *Server) executeWithCancelForSession(
	args [][]byte,
	cancel <-chan struct{},
	session *authSession,
) ([]byte, error) {
	return s.executeWithCancelSession(args, cancel, session)
}

func (s *Server) executeWithCancelSession(
	args [][]byte,
	cancel <-chan struct{},
	session *authSession,
) (response []byte, resultErr error) {
	atomic.AddUint64(&s.commands, 1)
	if atomic.LoadUint32(&s.metricsEnabled) != 0 {
		start := time.Now()
		name := "unknown"
		if len(args) > 0 {
			candidate := strings.ToUpper(string(args[0]))
			if _, ok := commandTable[candidate]; ok {
				name = candidate
			}
		}
		defer func() { s.metrics.Observe(name, time.Since(start), resultErr != nil) }()
	}

	// Redis allows FUNCTION STATS while a function is busy. It therefore cannot
	// wait on durableMu, which is intentionally held for the whole FCALL. HELP is
	// also pure introspection and can use the same direct path.
	if response, handled, err := s.executeFunctionIntrospection(args); handled {
		return response, err
	}

	// Blocking commands must not retain durableMu while sleeping. They wait
	// outside the command-serialization critical section, then execute the
	// eventual non-blocking mutation through executeDurable below.
	if isBlockingListCommand(args) {
		return s.executeBlockingList(args, cancel)
	}
	if isBlockingZSetCommand(args) {
		return s.executeBlockingZSet(args, cancel)
	}
	if isBlockingStreamCommand(args) {
		return s.executeBlockingStream(args, cancel)
	}
	return s.executeDurableForSession(args, session)
}

// executeAuthorizedConcurrentRawGet keeps the raw value in the engine arena and
// lets the TCP layer frame it directly into its existing connection buffer.
// It is intentionally limited to encoding-disabled stores; encoded values keep
// the ordinary GET path and its activity/codec semantics.
func (s *Server) executeAuthorizedConcurrentRawGet(
	args [][]byte,
	writeBulk func([]byte) error,
) (handled bool, err error) {
	if len(args) != 2 ||
		!bytes.EqualFold(args[0], []byte("GET")) ||
		s.journal != nil ||
		atomic.LoadUint32(&s.metricsEnabled) != 0 {
		return false, nil
	}

	s.durableMu.RLock()
	if s.hasWatchSessionsLocked() {
		s.durableMu.RUnlock()
		return false, nil
	}

	handled, err = s.store.VisitRawString(string(args[1]), func(value []byte) error {
		atomic.AddUint64(&s.commands, 1)
		return writeBulk(value)
	})
	s.durableMu.RUnlock()

	return handled, err
}

func (s *Server) executeAuthorizedConcurrentKnownGetInto(
	key []byte,
	dst []byte,
) (value []byte, found bool, handled bool, err error) {
	if s.journal != nil ||
		atomic.LoadUint32(&s.metricsEnabled) != 0 {
		return nil, false, false, nil
	}

	s.durableMu.RLock()
	if s.hasWatchSessionsLocked() {
		s.durableMu.RUnlock()
		return nil, false, false, nil
	}

	value, found, wrongType := s.store.GetStringBytesInto(key, dst)
	s.durableMu.RUnlock()

	atomic.AddUint64(&s.commands, 1)
	if wrongType {
		return nil, false, true, errWrongType
	}
	return value, found, true, nil
}

func (s *Server) executeAuthorizedConcurrentGetInto(
	args [][]byte,
	dst []byte,
) (value []byte, found bool, handled bool, err error) {
	if len(args) != 2 ||
		!bytes.EqualFold(args[0], []byte("GET")) ||
		s.journal != nil ||
		atomic.LoadUint32(&s.metricsEnabled) != 0 {
		return nil, false, false, nil
	}

	s.durableMu.RLock()
	if s.hasWatchSessionsLocked() {
		s.durableMu.RUnlock()
		return nil, false, false, nil
	}

	value, found, wrongType := s.store.GetStringInto(string(args[1]), dst)
	s.durableMu.RUnlock()

	atomic.AddUint64(&s.commands, 1)
	if wrongType {
		return nil, false, true, errWrongType
	}
	return value, found, true, nil
}

// executeAuthorizedConcurrentGetValue serves a previously ACL-authorized plain
// GET without formatting the successful bulk response. The TCP fast path can
// frame caller-owned decoded bytes directly into its existing connection buffer,
// avoiding a second value-sized allocation and copy after codec decode.
func (s *Server) executeAuthorizedConcurrentGetValue(args [][]byte) (value []byte, found bool, handled bool, err error) {
	if len(args) != 2 ||
		!bytes.EqualFold(args[0], []byte("GET")) ||
		s.journal != nil ||
		atomic.LoadUint32(&s.metricsEnabled) != 0 {
		return nil, false, false, nil
	}

	s.durableMu.RLock()
	if s.hasWatchSessionsLocked() {
		s.durableMu.RUnlock()
		return nil, false, false, nil
	}

	value, found, wrongType := s.store.GetString(string(args[1]))
	s.durableMu.RUnlock()

	atomic.AddUint64(&s.commands, 1)
	if wrongType {
		return nil, false, true, errWrongType
	}
	return value, found, true, nil
}

// executeAuthorizedConcurrentGet preserves the formatted-response helper used
// by in-process callers and tests. TCP uses executeAuthorizedConcurrentGetValue
// so successful GETs do not copy the decoded value into another response slice.
func (s *Server) executeAuthorizedConcurrentGet(args [][]byte) (response []byte, handled bool, err error) {
	value, found, handled, err := s.executeAuthorizedConcurrentGetValue(args)
	if !handled || err != nil {
		return nil, handled, err
	}
	return optionalBulk(value, found), true, nil
}

// executeAuthorizedConcurrentSet serves a previously ACL-authorized plain SET
// without passing through the generic function/blocking/pressure dispatch stack.
// It is only used when persistence, metrics, WATCH and maxmemory semantics do not
// require the ordinary durability/pressure path.
func (s *Server) executeAuthorizedConcurrentSet(args [][]byte) (response []byte, handled bool, err error) {
	if len(args) != 3 ||
		!bytes.EqualFold(args[0], []byte("SET")) ||
		s.journal != nil ||
		atomic.LoadUint32(&s.metricsEnabled) != 0 ||
		s.store.MaxMemory() != 0 {
		return nil, false, nil
	}

	s.durableMu.RLock()
	if s.hasWatchSessionsLocked() {
		s.durableMu.RUnlock()
		return nil, false, nil
	}

	key := string(args[1])
	setErr := s.store.SetPlain(key, args[2])
	s.durableMu.RUnlock()

	atomic.AddUint64(&s.commands, 1)
	if setErr != nil {
		return nil, true, setErr
	}
	if s.optimizer != nil && s.store.ShouldQueueOptimization(args[2]) {
		s.optimizer.Queue(key)
	}
	return []byte("+OK\r\n"), true, nil
}

func (s *Server) executeDurable(args [][]byte) ([]byte, error) {
	return s.executeDurableForSession(args, nil)
}

func (s *Server) executeDurableForSession(
	args [][]byte,
	session *authSession,
) ([]byte, error) {
	// GET and SET are single-key engine operations whose Store paths are already
	// concurrency-safe. When AOF is disabled and no WATCH session exists, they
	// do not need the global exclusive command lock. An RLock still excludes
	// MULTI/EXEC and every complex command, preserving transaction atomicity.
	if s.journal == nil && isConcurrentScalarCommand(args) {
		s.durableMu.RLock()
		if !s.hasWatchSessionsLocked() {
			result, err := s.executePressure(args)
			s.durableMu.RUnlock()
			return result, err
		}
		s.durableMu.RUnlock()
	}

	s.durableMu.Lock()
	defer s.durableMu.Unlock()

	return s.withExecutionACLContextLocked(session, args, func() ([]byte, error) {

	finishRunning := func() {}
	if isFunctionCallCommand(args) {
		finishRunning = beginRunningFunction(s, args)
	}
	defer finishRunning()

	// WATCH must observe every logical change, including a change that is later
	// restored to the original value by another command. Refresh both before and
	// after each serialized command so expiration cleanup that happened between
	// commands is also visible.
	s.refreshWatchesLocked()
	result, err := s.executeDurableLocked(args)
	s.refreshWatchesLocked()
	return result, err
	})
}

// executeDurableLocked executes one command while durableMu is already held.
// Transaction execution uses a different durability path so the whole EXEC is
// appended as one persistence frame rather than one frame per queued command.
func (s *Server) executeDurableLocked(args [][]byte) ([]byte, error) {
	if s.journal == nil {
		result, err := s.executePressure(args)
		if err == nil {
			s.signalListAvailability(args, result)
			s.signalZSetAvailability(args, result)
			s.signalStreamAvailability(args, result)
		}
		return result, err
	}
	if len(args) == 0 {
		return s.executePressure(args)
	}
	cmd := strings.ToUpper(string(args[0]))
	info, ok := commandTable[cmd]
	if !ok || !info.write {
		return s.executePressure(args)
	}

	// SORT is only mutating when STORE is present. Keep ordinary SORT usable as
	// a read even if the journal has failed, and avoid writing redundant AOF
	// records for read-only invocations.
	var sortDestination string
	if cmd == "SORT" {
		var hasStore bool
		sortDestination, hasStore = sortStoreDestination(args)
		if !hasStore {
			return s.executePressure(args)
		}
	}

	if s.durabilityFailed {
		return nil, errors.New("ERR persistence is unavailable; restart after repairing storage")
	}

	if cmd == "MIGRATE" {
		return s.executeMigrateDurableLocked(args)
	}

	// EVAL/EVALSHA and FCALL have dynamic key access, and Redis keeps mutations
	// performed before a later Lua runtime error. Snapshot/diff the complete
	// logical DB and append the resulting changes as one persistence frame.
	if isScriptEvalCommand(args) || isWritableFunctionCallCommand(args) {
		return s.executeScriptDurableLocked(args)
	}

	if cmd == "FLUSHDB" || cmd == "FLUSHALL" {
		before := s.store.Export(nil)

		result, err := s.executePressure(args)
		if err != nil {
			return result, err
		}

		reset := []persistence.Record{{Reset: true}}
		if err = s.journal.Append(reset); err != nil {
			s.durabilityFailed = true
			rollback := append([]persistence.Record{{Reset: true}}, before...)
			if rollbackErr := s.store.Restore(rollback, true); rollbackErr != nil {
				return nil, errors.New("ERR persistence and rollback failed")
			}
			return nil, errors.New("ERR persistence append failed")
		}

		s.signalListAvailability(args, result)
		s.signalZSetAvailability(args, result)
		s.signalStreamAvailability(args, result)
		return result, nil
	}

	// Validate arity before deriving the affected key set.
	if len(args) < info.min || info.max > 0 && len(args) > info.max {
		return s.executePressure(args)
	}
	var affected []string
	if cmd == "SORT" {
		affected = []string{sortDestination}
	} else if cmd == "COPY" && len(args) >= 3 {
		// COPY never mutates the source key. Persist and rollback only the
		// destination so a failed AOF append cannot disturb the source.
		affected = []string{string(args[2])}
	} else if cmd == "ZMPOP" {
		affected = zsetMPopKeys(args)
	} else if cmd == "XREADGROUP" {
		affected = streamGroupReadKeys(args)
	} else {
		last := info.last
		if last < 0 {
			last = len(args) + last
		}
		for i := info.first; i > 0 && i <= last && i < len(args); i += info.step {
			affected = append(affected, string(args[i]))
		}
	}
	before := s.store.Export(affected)
	result, err := s.executePressure(args)
	if err != nil {
		return result, err
	}
	after := s.store.Export(affected)
	if err = s.journal.Append(after); err != nil {
		s.durabilityFailed = true
		if rollbackErr := s.store.Restore(before, true); rollbackErr != nil {
			return nil, errors.New("ERR persistence and rollback failed")
		}
		return nil, errors.New("ERR persistence append failed")
	}
	s.signalListAvailability(args, result)
	s.signalZSetAvailability(args, result)
	s.signalStreamAvailability(args, result)
	return result, nil
}

func isConcurrentScalarCommand(args [][]byte) bool {
	if len(args) == 2 && bytes.EqualFold(args[0], []byte("GET")) {
		return true
	}
	// Keep only the plain SET key value form on the concurrent fast path.
	// Option parsing can involve TTL/conditional semantics and stays on the
	// serialized path until separately audited.
	return len(args) == 3 && bytes.EqualFold(args[0], []byte("SET"))
}
