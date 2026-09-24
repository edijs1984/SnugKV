package server

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"snugkv/internal/persistence"
	"strconv"
	"strings"
)

func readRedisReplicationCommand(reader *bufio.Reader) ([][]byte, int64, error) {
	prefix, err := reader.ReadByte()
	if err != nil {
		return nil, 0, err
	}
	count := int64(1)
	if prefix != '*' {
		line, _ := reader.ReadString('\n')
		return nil, count + int64(len(line)), fmt.Errorf("unexpected Redis replication prefix %q", string(prefix)+line)
	}

	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, 0, err
	}
	count += int64(len(line))
	argc, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || argc < 0 || argc > 1<<20 {
		return nil, 0, errors.New("invalid Redis replication array length")
	}

	args := make([][]byte, 0, argc)
	for i := 0; i < argc; i++ {
		p, err := reader.ReadByte()
		if err != nil {
			return nil, 0, err
		}
		count++
		if p != '$' {
			return nil, 0, errors.New("Redis replication command argument is not bulk string")
		}
		lengthLine, err := reader.ReadString('\n')
		if err != nil {
			return nil, 0, err
		}
		count += int64(len(lengthLine))
		n, err := strconv.Atoi(strings.TrimSpace(lengthLine))
		if err != nil || n < 0 || n > 128<<20 {
			return nil, 0, errors.New("invalid Redis replication bulk length")
		}
		arg := make([]byte, n)
		if _, err := io.ReadFull(reader, arg); err != nil {
			return nil, 0, err
		}
		count += int64(n)
		var crlf [2]byte
		if _, err := io.ReadFull(reader, crlf[:]); err != nil {
			return nil, 0, err
		}
		count += 2
		if crlf != [2]byte{'\r', '\n'} {
			return nil, 0, errors.New("invalid Redis replication bulk terminator")
		}
		args = append(args, arg)
	}
	return args, count, nil
}

func redisReplicationAffectedKeys(commands [][][]byte) (keys []string, full bool) {
	seen := make(map[string]struct{})
	add := func(key string) {
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}

	for _, args := range commands {
		if len(args) == 0 {
			continue
		}
		cmd := strings.ToUpper(string(args[0]))
		switch cmd {
		case "PING", "SELECT", "REPLCONF":
			continue
		case "FLUSHDB", "FLUSHALL":
			return nil, true
		}
		if isScriptEvalCommand(args) || isWritableFunctionCallCommand(args) || cmd == "MIGRATE" {
			return nil, true
		}
		if cmd == "SORT" {
			if destination, ok := sortStoreDestination(args); ok {
				add(destination)
			}
			continue
		}
		if cmd == "COPY" && len(args) >= 3 {
			add(string(args[2]))
			continue
		}
		if cmd == "ZMPOP" {
			for _, key := range zsetMPopKeys(args) {
				add(key)
			}
			continue
		}
		if cmd == "XREADGROUP" {
			for _, key := range streamGroupReadKeys(args) {
				add(key)
			}
			continue
		}

		info, ok := commandTable[cmd]
		if !ok || !info.write {
			continue
		}
		if info.first <= 0 {
			return nil, true
		}
		last := info.last
		if last < 0 {
			last = len(args) + last
		}
		for i := info.first; i <= last && i < len(args); i += info.step {
			add(string(args[i]))
		}
	}
	return keys, false
}

func (s *Server) applyRedisReplicationBatch(commands [][][]byte, checkpointOffsets ...int64) error {
	if len(commands) == 0 {
		return nil
	}
	persistCheckpoint := len(checkpointOffsets) > 0
	var checkpointOffset int64
	if persistCheckpoint {
		checkpointOffset = checkpointOffsets[0]
	}
	s.durableMu.Lock()
	defer s.durableMu.Unlock()

	keys, full := redisReplicationAffectedKeys(commands)
	var before []persistence.Record
	if full {
		before = s.store.Export(nil)
	} else if len(keys) > 0 {
		before = s.store.Export(keys)
	}

	s.refreshWatchesLocked()
	for _, args := range commands {
		if len(args) == 0 {
			continue
		}
		cmd := strings.ToUpper(string(args[0]))
		switch cmd {
		case "PING":
			continue
		case "SELECT":
			if len(args) != 2 || string(args[1]) != "0" {
				return errors.New("Redis replication selected unsupported nonzero database")
			}
			continue
		case "REPLCONF":
			continue
		}
		if _, err := s.executePressure(args); err != nil {
			if full || len(keys) > 0 {
				rollback := before
				if full {
					rollback = append([]persistence.Record{{Reset: true}}, before...)
				}
				_ = s.store.Restore(rollback, true)
			}
			return fmt.Errorf("apply Redis replication command %s: %w", cmd, err)
		}
	}
	s.refreshWatchesLocked()

	if s.journal == nil || !persistCheckpoint {
		return nil
	}

	var after []persistence.Record
	if full {
		after = s.store.Export(nil)
	} else if len(keys) > 0 {
		after = s.store.Export(keys)
	}
	changes := persistenceDiff(before, after)
	if len(changes) == 0 {
		return nil
	}
	checkpoint := s.replicationCheckpointForOffset(checkpointOffset, true)
	if checkpoint == nil {
		return errors.New("missing replication checkpoint state")
	}
	records := append(changes, persistence.Record{Replication: checkpoint})
	if err := s.journal.Append(records); err != nil {
		s.durabilityFailed = true
		rollback := before
		if full {
			rollback = append([]persistence.Record{{Reset: true}}, before...)
		}
		if rollbackErr := s.store.Restore(rollback, true); rollbackErr != nil {
			return errors.New("replication persistence and rollback failed")
		}
		return errors.New("replication persistence append failed")
	}
	return nil
}
