package server

import (
	"errors"

	"snugkv/internal/persistence"
)

// runAtomicScript runs a writable script or function. When the script asked for
// the atomic flag, or the server runs with -atomic-transactions, a failure
// undoes every write the script made before it failed. Otherwise the Redis
// behaviour applies: writes made before a runtime error stay.
//
// The caller holds the exclusive command lock, so no other client can write
// between the snapshot and the rollback. A script can touch any key, so the
// snapshot is of the whole keyspace.
func (s *Server) runAtomicScript(requested bool, run func() ([]byte, error)) ([]byte, error) {
	if !requested && !s.atomicTransactions {
		return run()
	}
	before := s.store.Export(nil)
	result, err := run()
	// A script can fail by raising an error or by returning an error reply
	// (redis.error_reply, or a redis.call failure that reaches the caller).
	if err == nil && (len(result) == 0 || result[0] != '-') {
		return result, nil
	}
	rollback := append([]persistence.Record{{Reset: true}}, before...)
	if rollbackErr := s.store.Restore(rollback, true); rollbackErr != nil {
		s.durabilityFailed = true
		return nil, errors.New("ERR atomic script failed and rollback failed; restart to recover from the log")
	}
	return result, err
}
