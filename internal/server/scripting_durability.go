package server

import (
	"errors"
	"snugkv/internal/persistence"
)

// executeScriptDurableLocked runs while durableMu is already held. Script
// runtime errors do not roll back earlier redis.call() mutations, matching Redis
// semantics, so the post-script logical diff must be persisted even when runErr
// is non-nil.
func (s *Server) executeScriptDurableLocked(args [][]byte) ([]byte, error) {
	before := s.store.Export(nil)
	result, runErr := s.executePressure(args)
	after := s.store.Export(nil)
	changes := persistenceDiff(before, after)

	if len(changes) > 0 {
		if err := s.journal.Append(changes); err != nil {
			s.durabilityFailed = true
			rollback := append([]persistence.Record{{Reset: true}}, before...)
			if rollbackErr := s.store.Restore(rollback, true); rollbackErr != nil {
				return nil, errors.New("ERR persistence and rollback failed")
			}
			return nil, errors.New("ERR persistence append failed")
		}
	}
	return result, runErr
}
