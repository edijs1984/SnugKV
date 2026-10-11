package server

import (
	"errors"

	"snugkv/internal/persistence"
)

// streamDeltaCommands are the stream commands whose effect is journaled as small
// per-stream deltas instead of the whole stream.
var streamDeltaCommands = map[string]bool{
	"XADD": true, "XDEL": true, "XTRIM": true, "XACK": true, "XGROUP": true,
	"XREADGROUP": true, "XCLAIM": true, "XAUTOCLAIM": true, "XDELEX": true, "XACKDEL": true,
}

// executeStreamDeltaLocked runs a stream command and journals/replicates only
// what it changed. handled is false when a delta cannot be used (the caller then
// falls back to exporting the affected keys whole); nothing has run in that case.
func (s *Server) executeStreamDeltaLocked(args [][]byte, affected []string) (result []byte, err error, handled bool) {
	tx, ok := s.store.BeginStreamTx(affected)
	if !ok {
		return nil, nil, false
	}
	defer tx.End()
	result, err = s.executePressure(args)
	if err != nil {
		return result, err, true
	}
	records, ok := tx.Records()
	if !ok {
		// Some change cannot be described as a delta; journal the keys whole.
		records = s.store.Export(affected)
	}
	if len(records) > 0 {
		if s.journal != nil {
			if appendErr := s.journal.Append(records); appendErr != nil {
				s.durabilityFailed = true
				if rollbackErr := tx.Rollback(); rollbackErr != nil {
					return nil, errors.New("ERR persistence and rollback failed"), true
				}
				return nil, errors.New("ERR persistence append failed"), true
			}
		}
		s.publishReplication(records)
	}
	s.signalListAvailability(args, result)
	s.signalZSetAvailability(args, result)
	s.signalStreamAvailability(args, result)
	return result, nil, true
}

var _ = persistence.Record{}

// streamCommandKeys lists the stream keys a delta command may change.
func streamCommandKeys(cmd string, args [][]byte, info commandInfo) []string {
	if cmd == "XREADGROUP" {
		return streamGroupReadKeys(args)
	}
	var keys []string
	last := info.last
	if last < 0 {
		last = len(args) + last
	}
	for i := info.first; i > 0 && i <= last && i < len(args); i += info.step {
		keys = append(keys, string(args[i]))
	}
	return keys
}
