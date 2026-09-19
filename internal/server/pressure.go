package server

import (
	"bytes"
	"errors"
	"snugkv/internal/engine"
	"snugkv/internal/persistence"
	"strconv"
	"strings"
)

func (s *Server) rejectDenyOOMCommand(args [][]byte) error {
	if len(args) == 0 || s.eviction != "noeviction" {
		return nil
	}

	name := strings.ToUpper(string(args[0]))
	if !commandDenyOOM(name) {
		return nil
	}

	memory := s.store.Memory()
	if memory.MaxBytes > 0 && memory.AccountedBytes > memory.MaxBytes {
		return engine.ErrOOM
	}

	return nil
}

func (s *Server) withNonDenyOOMBypass(
	args [][]byte,
	run func() ([]byte, error),
) ([]byte, error) {
	if len(args) == 0 || s.eviction != "noeviction" {
		return run()
	}

	name := strings.ToUpper(string(args[0]))
	info, ok := commandTable[name]
	if !ok ||
		!info.write ||
		commandDenyOOM(name) ||
		isScriptEvalCommand(args) ||
		isReadOnlyScriptEvalCommand(args) ||
		isFunctionCallCommand(args) {

		return run()
	}

	memory := s.store.Memory()
	if memory.MaxBytes == 0 || memory.AccountedBytes <= memory.MaxBytes {
		return run()
	}

	// Redis allows ordinary write commands without the denyoom flag to execute
	// while already over maxmemory. Some SnugKV data-structure rewrites may
	// temporarily need allocation even for logically shrinking/non-growing
	// commands (for example LPOP or RENAME).
	//
	// Control-plane commands such as CONFIG and scripting/Function invocations
	// are deliberately excluded: they either do not need engine admission or
	// have their own Redis-specific OOM entry semantics.
	maxMemory := s.store.MaxMemory()
	s.store.SetMaxMemory(0)
	defer s.store.SetMaxMemory(maxMemory)

	return run()
}

func (s *Server) executePressureCommand(args [][]byte) ([]byte, error) {
	if len(args) > 0 && strings.EqualFold(string(args[0]), "FUNCTION") {
		return s.executeFunctionTopLevel(args)
	}
	if isFunctionCommand(args) {
		if scriptExecutionActive(s) {
			return nil, errors.New("ERR This Redis command is not allowed from script")
		}
		if isFunctionCallCommand(args) {
			leave := enterScriptExecution(s)
			defer leave()
			return s.executeKillableFunctionCall(args)
		}
		return s.executeFunctionCommand(args)
	}
	if isReadOnlyScriptEvalCommand(args) {
		if scriptExecutionActive(s) {
			return nil, errors.New("ERR This Redis command is not allowed from script")
		}
		leave := enterScriptExecution(s)
		defer leave()
		return s.executeKillableScripting(args)
	}
	if isScriptingCommand(args) {
		if isScriptEvalCommand(args) {
			leave := enterScriptExecution(s)
			defer leave()
			return s.executeKillableScripting(args)
		}
		return s.executeScripting(args)
	}
	if isSortCommand(args) {
		return s.executeSort(args)
	}
	if isCopyCommand(args) {
		return s.executeCopy(args)
	}
	if isMigrateCommand(args) {
		return s.executeMigrate(args)
	}
	if isKeyDumpRestoreCommand(args) {
		return s.executeKeyDumpRestore(args)
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
	// Plain GET/SET dominate normal cache workloads. Avoid sending them through
	// every scripting/container/special-command predicate in the generic router.
	// GET still enforces Redis WRONGTYPE semantics. Plain SET overwrites any type.
	if len(args) == 2 && bytes.EqualFold(args[0], []byte("GET")) {
		const prefixReserve = 32

		response := make([]byte, prefixReserve, prefixReserve+2)
		response, found, wrongType := s.store.AppendStringValue(string(args[1]), response)
		if wrongType {
			return nil, errWrongType
		}
		if !found {
			return nullBulk(), nil
		}

		valueLength := len(response) - prefixReserve
		var header [32]byte
		h := header[:0]
		h = append(h, 36)
		h = strconv.AppendInt(h, int64(valueLength), 10)
		h = append(h, 13, 10)

		start := prefixReserve - len(h)
		copy(response[start:prefixReserve], h)
		response = append(response, 13, 10)
		return response[start:], nil
	}
	if len(args) == 3 &&
		bytes.EqualFold(args[0], []byte("SET")) &&
		s.store.MaxMemory() == 0 {
		key := string(args[1])
		applied, _, _, err := s.store.SetWithOptions(
			key,
			args[2],
			engine.SetOptions{},
		)
		if err != nil {
			return nil, err
		}
		if applied && s.optimizer != nil {
			s.optimizer.Queue(key)
		}
		return []byte("+OK\r\n"), nil
	}

	if err := s.rejectDenyOOMCommand(args); err != nil {
		return nil, err
	}

	result, err := s.withNonDenyOOMBypass(
		args,
		func() ([]byte, error) {
			return s.executePressureCommand(args)
		},
	)
	if !errors.Is(err, engine.ErrOOM) || s.eviction == "" || s.eviction == "noeviction" {
		return result, err
	}
	// A script/function can successfully mutate data before a later redis.call()
	// hits OOM. Re-running the whole invocation would repeat those earlier side
	// effects, so only nested ordinary commands are eligible for eviction/retry.
	if isScriptEvalCommand(args) || isReadOnlyScriptEvalCommand(args) || isFunctionCallCommand(args) {
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
