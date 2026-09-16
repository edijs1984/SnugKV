package server

import (
	"bytes"
	"errors"
	"fmt"
	"snugkv/internal/persistence"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var transactionCommands = map[string]commandInfo{
	"MULTI":   {1, 1, 0, 0, 0, false},
	"EXEC":    {1, 1, 0, 0, 0, false},
	"DISCARD": {1, 1, 0, 0, 0, false},
	"WATCH":   {2, 0, 1, -1, 1, false},
	"UNWATCH": {1, 1, 0, 0, 0, false},
}

func init() {
	for name, info := range transactionCommands {
		commandTable[name] = info
	}
}

type transactionSession struct {
	server     *Server
	multi      bool
	queueDirty bool
	queue      [][][]byte
	watched    map[string]persistence.Record
	watchDirty bool
}

type transactionWatchRegistry struct {
	sessions map[*transactionSession]struct{}
}

var transactionWatchRegistries sync.Map // map[*Server]*transactionWatchRegistry

func transactionRegistryForServer(s *Server) *transactionWatchRegistry {
	if existing, ok := transactionWatchRegistries.Load(s); ok {
		return existing.(*transactionWatchRegistry)
	}
	created := &transactionWatchRegistry{sessions: make(map[*transactionSession]struct{})}
	actual, _ := transactionWatchRegistries.LoadOrStore(s, created)
	return actual.(*transactionWatchRegistry)
}

func newTransactionSession(s *Server) *transactionSession {
	return &transactionSession{server: s, watched: make(map[string]persistence.Record)}
}

func cloneCommand(args [][]byte) [][]byte {
	out := make([][]byte, len(args))
	for i := range args {
		out[i] = append([]byte(nil), args[i]...)
	}
	return out
}

func persistenceRecordEqual(a, b persistence.Record) bool {
	return a.Reset == b.Reset &&
		a.ExpiresAtMS == b.ExpiresAtMS &&
		a.Deleted == b.Deleted &&
		a.ValueType == b.ValueType &&
		bytes.Equal(a.Key, b.Key) &&
		bytes.Equal(a.Value, b.Value)
}

func recordMap(records []persistence.Record) map[string]persistence.Record {
	out := make(map[string]persistence.Record, len(records))
	for _, record := range records {
		out[string(record.Key)] = record
	}
	return out
}

func persistenceDiff(before, after []persistence.Record) []persistence.Record {
	beforeMap := recordMap(before)
	afterMap := recordMap(after)
	keys := make([]string, 0, len(beforeMap)+len(afterMap))
	seen := make(map[string]struct{}, len(beforeMap)+len(afterMap))
	for key := range beforeMap {
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for key := range afterMap {
		if _, ok := seen[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	changes := make([]persistence.Record, 0)
	for _, key := range keys {
		old, oldOK := beforeMap[key]
		current, currentOK := afterMap[key]
		if oldOK && currentOK && persistenceRecordEqual(old, current) {
			continue
		}
		if !currentOK {
			changes = append(changes, persistence.Record{Key: []byte(key), Deleted: true})
			continue
		}
		changes = append(changes, current)
	}
	return changes
}

func (s *Server) refreshWatchesLocked() {
	registry := transactionRegistryForServer(s)
	for session := range registry.sessions {
		if session.watchDirty || len(session.watched) == 0 {
			continue
		}
		keys := make([]string, 0, len(session.watched))
		for key := range session.watched {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		current := recordMap(s.store.Export(keys))
		for _, key := range keys {
			if !persistenceRecordEqual(session.watched[key], current[key]) {
				session.watchDirty = true
				break
			}
		}
	}
}

func (session *transactionSession) clearWatchLocked() {
	registry := transactionRegistryForServer(session.server)
	delete(registry.sessions, session)
	session.watched = make(map[string]persistence.Record)
	session.watchDirty = false
}

func (session *transactionSession) clearMultiLocked() {
	session.multi = false
	session.queueDirty = false
	session.queue = nil
}

func (session *transactionSession) close() {
	s := session.server
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	session.clearWatchLocked()
	session.clearMultiLocked()
}

func (session *transactionSession) watch(keys [][]byte) ([]byte, error) {
	s := session.server
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	if session.multi {
		return nil, errors.New("ERR WATCH inside MULTI is not allowed")
	}
	s.refreshWatchesLocked()
	newKeys := make([]string, 0, len(keys))
	for _, raw := range keys {
		key := string(raw)
		if _, exists := session.watched[key]; !exists {
			newKeys = append(newKeys, key)
		}
	}
	if len(newKeys) > 0 {
		records := s.store.Export(newKeys)
		for _, record := range records {
			session.watched[string(record.Key)] = record
		}
		transactionRegistryForServer(s).sessions[session] = struct{}{}
	}
	return []byte("+OK\r\n"), nil
}

func (session *transactionSession) unwatch() ([]byte, error) {
	s := session.server
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	s.refreshWatchesLocked()
	session.clearWatchLocked()
	return []byte("+OK\r\n"), nil
}

func queuedCommandValidation(args [][]byte) error {
	if len(args) == 0 {
		return errors.New("ERR empty command")
	}
	cmd := strings.ToUpper(string(args[0]))
	info, ok := commandTable[cmd]
	if !ok {
		return fmt.Errorf("ERR unknown command '%s'", cmd)
	}
	if len(args) < info.min || info.max > 0 && len(args) > info.max {
		return fmt.Errorf("ERR wrong number of arguments for '%s' command", strings.ToLower(cmd))
	}
	// These commands mutate connection subscription state and emit push frames,
	// which cannot be represented as ordinary elements in a RESP2 EXEC array.
	// Keep them as an explicit compatibility limitation rather than changing
	// subscription state while a transaction is being accumulated.
	switch cmd {
	case "SUBSCRIBE", "UNSUBSCRIBE", "PSUBSCRIBE", "PUNSUBSCRIBE", "SSUBSCRIBE", "SUNSUBSCRIBE", "RESET", "QUIT":
		return fmt.Errorf("ERR command '%s' is not allowed inside MULTI", strings.ToLower(cmd))
	}
	return nil
}

func (session *transactionSession) queueCommand(args [][]byte) ([]byte, error) {
	if err := queuedCommandValidation(args); err != nil {
		session.queueDirty = true
		return nil, err
	}
	session.queue = append(session.queue, cloneCommand(args))
	return []byte("+QUEUED\r\n"), nil
}

func (session *transactionSession) discard() ([]byte, error) {
	s := session.server
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	if !session.multi {
		return nil, errors.New("ERR DISCARD without MULTI")
	}
	session.clearMultiLocked()
	session.clearWatchLocked()
	return []byte("+OK\r\n"), nil
}

func transactionHasWrites(commands [][][]byte) bool {
	for _, args := range commands {
		if len(args) == 0 {
			continue
		}
		if info, ok := commandTable[strings.ToUpper(string(args[0]))]; ok && info.write {
			return true
		}
	}
	return false
}

func (s *Server) executeQueuedBlockingListLocked(args [][]byte) ([]byte, error) {
	cmd := strings.ToUpper(string(args[0]))
	switch cmd {
	case "BLPOP", "BRPOP":
		left := cmd == "BLPOP"
		for _, key := range args[1 : len(args)-1] {
			op := "RPOP"
			if left {
				op = "LPOP"
			}
			response, err := s.executeTransactionPressure([][]byte{[]byte(op), key})
			if err != nil {
				return nil, err
			}
			if string(response) != "$-1\r\n" {
				return array(formatBulkString(key), response), nil
			}
		}
		return []byte("*-1\r\n"), nil
	case "BLMOVE":
		return s.executeTransactionPressure([][]byte{[]byte("LMOVE"), args[1], args[2], args[3], args[4]})
	case "BRPOPLPUSH":
		return s.executeTransactionPressure([][]byte{[]byte("RPOPLPUSH"), args[1], args[2]})
	}
	return nil, errors.New("ERR unsupported blocking list command in MULTI")
}

func (s *Server) executeQueuedBlockingZSetLocked(args [][]byte) ([]byte, error) {
	cmd := strings.ToUpper(string(args[0]))
	switch cmd {
	case "BZPOPMIN", "BZPOPMAX":
		op := "ZPOPMIN"
		if cmd == "BZPOPMAX" {
			op = "ZPOPMAX"
		}
		for _, key := range args[1 : len(args)-1] {
			response, err := s.executeTransactionPressure([][]byte{[]byte(op), key})
			if err != nil {
				return nil, err
			}
			if string(response) == "*0\r\n" {
				continue
			}
			if !bytes.HasPrefix(response, []byte("*2\r\n")) {
				return nil, errors.New("ERR internal zset pop response")
			}
			out := []byte("*3\r\n")
			out = append(out, formatBulkString(key)...)
			out = append(out, response[len("*2\r\n"):]...)
			return out, nil
		}
		return []byte("*-1\r\n"), nil
	case "BZMPOP":
		command := make([][]byte, 0, len(args)-1)
		command = append(command, []byte("ZMPOP"))
		command = append(command, args[2:]...)
		return s.executeTransactionPressure(command)
	}
	return nil, errors.New("ERR unsupported blocking sorted-set command in MULTI")
}

func (s *Server) executeQueuedCommandLocked(args [][]byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("ERR empty command")
	}
	cmd := strings.ToUpper(string(args[0]))
	if cmd == "UNWATCH" {
		// Redis OSS allows UNWATCH inside MULTI, but it has no effect because
		// EXEC already clears WATCH state before executing queued commands.
		return []byte("+OK\r\n"), nil
	}
	if isBlockingListCommand(args) {
		return s.executeQueuedBlockingListLocked(args)
	}
	if isBlockingZSetCommand(args) {
		return s.executeQueuedBlockingZSetLocked(args)
	}
	// XREAD/XREADGROUP with BLOCK naturally execute non-blockingly here because
	// the blocking wrapper lives in ExecuteWithCancel, above executePressure.
	return s.executeTransactionPressure(args)
}

func (session *transactionSession) exec() ([]byte, error) {
	s := session.server
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	if !session.multi {
		return nil, errors.New("ERR EXEC without MULTI")
	}
	if session.queueDirty {
		session.clearMultiLocked()
		session.clearWatchLocked()
		return nil, errors.New("EXECABORT Transaction discarded because of previous errors.")
	}

	s.refreshWatchesLocked()
	if session.watchDirty {
		session.clearMultiLocked()
		session.clearWatchLocked()
		return []byte("*-1\r\n"), nil
	}

	commands := session.queue
	session.clearMultiLocked()
	// EXEC always unwatches before running the queued commands, so writes in the
	// transaction itself do not make its own WATCH condition fail.
	session.clearWatchLocked()

	writes := transactionHasWrites(commands)
	var before []persistence.Record
	if s.journal != nil && writes {
		before = s.store.Export(nil)
	}

	results := make([][]byte, 0, len(commands))
	for _, command := range commands {
		cmd := strings.ToUpper(string(command[0]))
		info := commandTable[cmd]
		var result []byte
		var err error
		if s.journal != nil && s.durabilityFailed && info.write {
			err = errors.New("ERR persistence is unavailable; restart after repairing storage")
		} else {
			result, err = s.executeQueuedCommandLocked(command)
		}
		if err != nil {
			result = errorResponse(err)
		} else {
			s.signalListAvailability(command, result)
			s.signalZSetAvailability(command, result)
			s.signalStreamAvailability(command, result)
		}
		results = append(results, result)
		// Preserve Redis WATCH semantics for other clients even if a later
		// command in this same transaction restores the previous value.
		s.refreshWatchesLocked()
	}

	if s.journal != nil && writes && !s.durabilityFailed {
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
	}
	return array(results...), nil
}

func (session *transactionSession) handleCommand(args [][]byte) (bool, []byte, error) {
	if len(args) == 0 {
		return false, nil, nil
	}
	cmd := strings.ToUpper(string(args[0]))
	switch cmd {
	case "MULTI":
		if len(args) != 1 {
			return true, nil, errors.New("ERR wrong number of arguments for 'multi' command")
		}
		if session.multi {
			return true, nil, errors.New("ERR MULTI calls can not be nested")
		}
		session.multi = true
		session.queueDirty = false
		session.queue = nil
		return true, []byte("+OK\r\n"), nil
	case "EXEC":
		if len(args) != 1 {
			return true, nil, errors.New("ERR wrong number of arguments for 'exec' command")
		}
		response, err := session.exec()
		return true, response, err
	case "DISCARD":
		if len(args) != 1 {
			return true, nil, errors.New("ERR wrong number of arguments for 'discard' command")
		}
		response, err := session.discard()
		return true, response, err
	case "WATCH":
		if len(args) < 2 {
			return true, nil, errors.New("ERR wrong number of arguments for 'watch' command")
		}
		response, err := session.watch(args[1:])
		return true, response, err
	case "UNWATCH":
		if len(args) != 1 {
			if session.multi {
				session.queueDirty = true
			}
			return true, nil, errors.New("ERR wrong number of arguments for 'unwatch' command")
		}
		if session.multi {
			response, err := session.queueCommand(args)
			return true, response, err
		}
		response, err := session.unwatch()
		return true, response, err
	default:
		if !session.multi {
			return false, nil, nil
		}
		response, err := session.queueCommand(args)
		return true, response, err
	}
}

func (s *Server) observeTransactionCommand(args [][]byte, started time.Time, err error) {
	atomic.AddUint64(&s.commands, 1)
	name := "unknown"
	if len(args) > 0 {
		candidate := strings.ToUpper(string(args[0]))
		if _, ok := commandTable[candidate]; ok {
			name = candidate
		}
	}
	s.metrics.Observe(name, time.Since(started), err != nil)
}

func (s *Server) executeTransactionConnectionCommand(session *transactionSession, args [][]byte) (handled bool, response []byte, err error) {
	started := time.Now()
	handled, response, err = session.handleCommand(args)
	if handled {
		s.observeTransactionCommand(args, started, err)
	}
	return handled, response, err
}
