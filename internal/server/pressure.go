package server

import (
	"errors"
	"morphcache/internal/engine"
	"morphcache/internal/persistence"
	"strings"
)

func (s *Server) executePressure(args [][]byte) ([]byte, error) {
	result, err := s.execute(args)
	if !errors.Is(err, engine.ErrOOM) || s.eviction == "" || s.eviction == "noeviction" {
		return result, err
	}
	cmd := strings.ToUpper(string(args[0]))
	info := commandTable[cmd]
	excluded := make(map[string]bool)
	last := info.last
	if last < 0 {
		last = len(args) + last
	}
	for i := info.first; i > 0 && i <= last && i < len(args); i += info.step {
		excluded[string(args[i])] = true
	}
	s.store.CleanupExpiredLimit(1024)
	s.store.Compact(64 << 20)
	result, err = s.execute(args)
	for n := 0; n < 128 && errors.Is(err, engine.ErrOOM); n++ {
		key, ok := s.store.Victim(excluded, s.eviction == "volatile-lru")
		if !ok {
			break
		}
		// Durable eviction is logged before deletion and is independent of whether
		// the pending client write eventually succeeds.
		if s.journal != nil {
			if journalErr := s.journal.Append([]persistence.Record{{Key: []byte(key), Deleted: true}}); journalErr != nil {
				s.durabilityFailed = true
				return nil, errors.New("ERR persistence append failed during eviction")
			}
		}
		s.store.Evict(key)
		s.store.Compact(64 << 20)
		result, err = s.execute(args)
	}
	return result, err
}
