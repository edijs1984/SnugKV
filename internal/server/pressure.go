package server

import (
	"errors"
	"snugkv/internal/engine"
	"snugkv/internal/persistence"
	"strings"
)

func (s *Server) executePressureCommand(args [][]byte) ([]byte, error) {
	if isScriptingCommand(args) {
		return s.executeScripting(args)
	}
	if isSortCommand(args) {
		return s.executeSort(args)
	}
	if isPubSubServerCommand(args) {
		return s.executePubSubServer(args)
	}
	if isHyperLogLogCommand(args) {
		return s.executeHyperLogLog(args)
	}
	if isGeoCommand(args) {
		return s.executeGeo(args)
	}
	if isStreamRefPolicyCommand(args) {
		return s.executeStreamRefPolicy(args)
	}
	if isXReadCommand(args) {
		return s.executeXRead(args)
	}
	if isStreamGroupDeliveryCommand(args) {
		return s.executeStreamGroupDelivery(args)
	}
	if isStreamClaimCommand(args) {
		return s.executeStreamClaim(args)
	}
	if isStreamInfoCommand(args) {
		return s.executeStreamInfo(args)
	}
	if isStreamCommand(args) {
		return s.executeStream(args)
	}
	if isStreamGroupCommand(args) {
		return s.executeStreamGroup(args)
	}
	if response, handled, err := s.executeStreamRename(args); handled {
		return response, err
	}
	if isTypedScalarSpecial(args) {
		return s.executeTypedScalarSpecial(args)
	}
	if err := s.validateLegacyScalarTypes(args); err != nil {
		return nil, err
	}
	if isKeyspaceScanCommand(args) {
		return s.executeKeyspaceScan(args)
	}
	if isZSetAlgebraCommand(args) {
		return s.executeZSetAlgebra(args)
	}
	if isZSetOpsCommand(args) {
		return s.executeZSetOps(args)
	}
	return s.executeRoutedCommand(args)
}

func (s *Server) executePressure(args [][]byte) ([]byte, error) {
	return s.executePressureMode(args, true)
}

// executeTransactionPressure keeps eviction inside the transaction's single
// durability frame. Evicted keys are captured by the transaction snapshot diff
// rather than appended to the journal independently.
func (s *Server) executeTransactionPressure(args [][]byte) ([]byte, error) {
	return s.executePressureMode(args, false)
}

func (s *Server) executePressureMode(args [][]byte, journalEvictions bool) ([]byte, error) {
	result, err := s.executePressureCommand(args)
	if !errors.Is(err, engine.ErrOOM) || s.eviction == "" || s.eviction == "noeviction" {
		return result, err
	}
	// A script can successfully mutate data before a later redis.call() hits OOM.
	// Re-running the whole script would repeat those earlier side effects, so only
	// nested ordinary commands are eligible for eviction/retry.
	if isScriptEvalCommand(args) {
		return result, err
	}
	cmd := strings.ToUpper(string(args[0]))
	info := commandTable[cmd]
	excluded := make(map[string]bool)
	if cmd == "XREADGROUP" {
		for _, key := range streamGroupReadKeys(args) {
			excluded[key] = true
		}
	} else if isSortCommand(args) {
		for _, key := range s.sortPressureKeys(args) {
			excluded[key] = true
		}
	} else if cmd == "GEOSEARCHSTORE" && len(args) >= 3 {
		// The destination is the only mutated key, but the source must remain
		// stable across an OOM/eviction retry as well.
		excluded[string(args[1])] = true
		excluded[string(args[2])] = true
	} else if isZSetAlgebraCommand(args) {
		for _, key := range zsetAlgebraInputKeys(args) {
			excluded[key] = true
		}
	} else if isZSetOpsCommand(args) {
		if keys := zsetOpsPressureKeys(args); len(keys) > 0 {
			for _, key := range keys {
				excluded[key] = true
			}
		} else {
			last := info.last
			if last < 0 {
				last = len(args) + last
			}
			for i := info.first; i > 0 && i <= last && i < len(args); i += info.step {
				excluded[string(args[i])] = true
			}
		}
	} else {
		last := info.last
		if last < 0 {
			last = len(args) + last
		}
		for i := info.first; i > 0 && i <= last && i < len(args); i += info.step {
			excluded[string(args[i])] = true
		}
	}
	s.store.CleanupExpiredLimit(1024)
	s.store.Compact(64 << 20)
	result, err = s.executePressureCommand(args)
	for n := 0; n < 128 && errors.Is(err, engine.ErrOOM); n++ {
		key, ok := s.store.Victim(excluded, s.eviction == "volatile-lru")
		if !ok {
			break
		}
		if journalEvictions && s.journal != nil {
			if journalErr := s.journal.Append([]persistence.Record{{Key: []byte(key), Deleted: true}}); journalErr != nil {
				s.durabilityFailed = true
				return nil, errors.New("ERR persistence append failed during eviction")
			}
		}
		s.store.Evict(key)
		s.store.Compact(64 << 20)
		result, err = s.executePressureCommand(args)
	}
	return result, err
}
