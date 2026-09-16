package server

import (
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
	return s.ExecuteWithCancel(args, nil)
}

// ExecuteWithCancel is identical to Execute except that blocking commands also
// stop when cancel is closed. Non-blocking commands intentionally ignore cancel.
// The TCP server uses this to release per-connection waiters when a peer goes
// away without changing the behavior of direct/in-process callers.
func (s *Server) ExecuteWithCancel(args [][]byte, cancel <-chan struct{}) (response []byte, resultErr error) {
	atomic.AddUint64(&s.commands, 1)
	start := time.Now()
	name := "unknown"
	if len(args) > 0 {
		candidate := strings.ToUpper(string(args[0]))
		if _, ok := commandTable[candidate]; ok {
			name = candidate
		}
	}
	defer func() { s.metrics.Observe(name, time.Since(start), resultErr != nil) }()

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
	return s.executeDurable(args)
}

func (s *Server) executeDurable(args [][]byte) ([]byte, error) {
	s.durableMu.Lock()
	defer s.durableMu.Unlock()

	// WATCH must observe every logical change, including a change that is later
	// restored to the original value by another command. Refresh both before and
	// after each serialized command so expiration cleanup that happened between
	// commands is also visible.
	s.refreshWatchesLocked()
	result, err := s.executeDurableLocked(args)
	s.refreshWatchesLocked()
	return result, err
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

	// Script keys are dynamic and a Redis script may keep mutations performed
	// before a later runtime error. Snapshot/diff the complete logical DB and
	// append the script's resulting changes as one persistence frame.
	if isScriptEvalCommand(args) {
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
