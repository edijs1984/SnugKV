package server

import (
	"errors"
	"morphcache/internal/persistence"
	"strings"
	"sync/atomic"
	"time"
)

type Journal interface {
	Append([]persistence.Record) error
}

// SetJournal is a startup-only operation. Durable commands are serialized so
// clients cannot observe a mutation whose journal append later fails.
func (s *Server) SetJournal(j Journal) { s.journal = j }
func (s *Server) Execute(args [][]byte) (response []byte, resultErr error) {
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
	if s.journal == nil {
		return s.executePressure(args)
	}
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	if len(args) == 0 {
		return s.executePressure(args)
	}
	cmd := strings.ToUpper(string(args[0]))
	info, ok := commandTable[cmd]
	if !ok || !info.write {
		return s.executePressure(args)
	}
	if s.durabilityFailed {
		return nil, errors.New("ERR persistence is unavailable; restart after repairing storage")
	}
	// Validate arity before deriving the affected key set.
	if len(args) < info.min || info.max > 0 && len(args) > info.max {
		return s.executePressure(args)
	}
	var affected []string
	last := info.last
	if last < 0 {
		last = len(args) + last
	}
	for i := info.first; i > 0 && i <= last && i < len(args); i += info.step {
		affected = append(affected, string(args[i]))
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
	return result, nil
}
