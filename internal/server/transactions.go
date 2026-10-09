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
	server                *Server
	auth                  *authSession
	multi                 bool
	atomic                bool
	queueDirty            bool
	queue                 [][][]byte
	watched               map[string]persistence.Record
	watchDirty            bool
	lastReplicationOffset int64
	lastDurabilitySequence uint64
	waitTargetOffset       int64
	waitAOFSequence        uint64
	clusterSlot            int
	clusterSlotSet         bool
	clusterCrossSlot       bool
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
	return &transactionSession{
		server:  s,
		watched: make(map[string]persistence.Record),
	}
}

func (session *transactionSession) markACLFailure() {
	if session.multi {
		session.queueDirty = true
	}
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

func (s *Server) hasWatchSessionsLocked() bool {
	return s.watchSessions.Load() != 0
}

func (s *Server) refreshWatchesLocked() {
	existing, ok := transactionWatchRegistries.Load(s)
	if !ok {
		return
	}
	registry := existing.(*transactionWatchRegistry)
	if len(registry.sessions) == 0 {
		return
	}
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
	if _, existed := registry.sessions[session]; existed {
		delete(registry.sessions, session)
		session.server.watchSessions.Add(-1)
	}
	session.watched = make(map[string]persistence.Record)
	session.watchDirty = false
}

func (session *transactionSession) clearMultiLocked() {
	session.multi = false
	session.atomic = false
	session.queueDirty = false
	session.queue = nil
	session.clusterSlot = 0
	session.clusterSlotSet = false
	session.clusterCrossSlot = false
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
	if s.clusterEnabled {
		args := make([][]byte, 0, len(keys)+1)
		args = append(args, []byte("WATCH"))
		args = append(args, keys...)
		if err := s.enforceClusterRouting(args); err != nil {
			return nil, err
		}
	}
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
		registry := transactionRegistryForServer(s)
		if _, existed := registry.sessions[session]; !existed {
			registry.sessions[session] = struct{}{}
			s.watchSessions.Add(1)
		}
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
	if session.atomic {
		if err := atomicQueueValidation(args); err != nil {
			session.queueDirty = true
			return nil, err
		}
	}

	if session.server.clusterEnabled {
		if err := session.server.enforceClusterRouting(args); err != nil {
			session.queueDirty = true
			return nil, err
		}

		keys, err := commandKeys(args)
		if err != nil {
			session.queueDirty = true
			return nil, err
		}
		if len(keys) > 0 {
			slot := clusterKeySlot(keys[0].value)
			if session.clusterSlotSet && session.clusterSlot != slot {
				// Redis Cluster still queues individually valid local commands in
				// MULTI even when they target different slots. The transaction-wide
				// slot constraint is enforced by EXEC.
				session.clusterCrossSlot = true
			}
			if !session.clusterSlotSet {
				session.clusterSlot = slot
				session.clusterSlotSet = true
			}
		}
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

func (s *Server) executeQueuedCommandLockedForSession(session *transactionSession, args [][]byte) ([]byte, error) {
	if len(args) > 0 && strings.EqualFold(string(args[0]), "WAIT") {
		return s.executeReplicationWait(args, session.waitTargetOffset, nil, false)
	}
	if len(args) > 0 && strings.EqualFold(string(args[0]), "WAITAOF") {
		return s.executeWaitAOF(args, session.waitTargetOffset, session.waitAOFSequence, nil, false)
	}
	return s.executeQueuedCommandLocked(args)
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

	if session.clusterCrossSlot {
		session.clearMultiLocked()
		session.clearWatchLocked()
		return nil, errors.New("CROSSSLOT Keys in request don't hash to the same slot")
	}

	// Redis re-checks ACL rules at EXEC time. A command that was legal when it
	// was queued must not execute if the user's ACL rules were changed before
	// EXEC.
	if session.auth != nil {
		for _, command := range session.queue {
			if err := s.authorizeConnectionCommand(session.auth, command); err != nil {
				session.clearMultiLocked()
				session.clearWatchLocked()

				return nil, errors.New(
					"NOPERM ACLs rules changed between the moment the transaction was accumulated and the EXEC call. This command is no longer allowed for the following reason: no permission to execute the command or subcommand",
				)
			}
		}
	}

	s.refreshWatchesLocked()
	if session.watchDirty {
		session.clearMultiLocked()
		session.clearWatchLocked()
		return []byte("*-1\r\n"), nil
	}

	commands := session.queue
	atomicTx := session.atomic
	session.clearMultiLocked()
	// EXEC always unwatches before running the queued commands, so writes in the
	// transaction itself do not make its own WATCH condition fail.
	session.clearWatchLocked()

	writes := transactionHasWrites(commands)
	replicate := writes && s.replication.primaryHasReplicas()
	var before []persistence.Record
	var snapshotKeys []string
	snapshotScoped := false
	if atomicTx && writes {
		// MULTI ATOMIC: remember the prior state of every key the transaction can
		// change so a failing command can undo the earlier ones. A transaction
		// whose key set cannot be known up front is snapshotted whole.
		snapshotKeys, snapshotScoped = atomicSnapshotKeys(commands)
		if snapshotScoped {
			before = s.store.Export(snapshotKeys)
		} else {
			before = s.store.Export(nil)
		}
	} else if (s.journal != nil || replicate) && writes {
		before = s.store.Export(nil)
	}
	exportAfter := func() []persistence.Record {
		if atomicTx && snapshotScoped {
			return s.store.Export(snapshotKeys)
		}
		return s.store.Export(nil)
	}
	restoreBefore := func() error {
		if atomicTx && snapshotScoped {
			return s.store.Restore(before, true)
		}
		return s.store.Restore(append([]persistence.Record{{Reset: true}}, before...), true)
	}

	results := make([][]byte, 0, len(commands))
	for index, command := range commands {
		commandStarted := time.Now()
		cmd := strings.ToUpper(string(command[0]))
		info := commandTable[cmd]
		var result []byte
		var err error
		if s.journal != nil && s.durabilityFailed && info.write {
			err = errors.New("ERR persistence is unavailable; restart after repairing storage")
		} else {
			if session.auth != nil && monitorScriptCommand(command) {
				s.feedMonitor(session.auth.client, command, nil)
			}
			result, err = s.withExecutionACLContextLocked(
				session.auth,
				command,
				func() ([]byte, error) {
					return s.executeQueuedCommandLockedForSession(session, command)
				},
			)
		}
		if err != nil && atomicTx && writes {
			if rollbackErr := restoreBefore(); rollbackErr != nil {
				s.durabilityFailed = true
				return nil, errors.New("ERR atomic transaction failed and rollback failed; restart to recover from the log")
			}
			s.refreshWatchesLocked()
			return nil, atomicAbortError(index, command[0], err)
		}
		if err != nil {
			result = errorResponse(err)
		} else if !atomicTx {
			s.signalListAvailability(command, result)
			s.signalZSetAvailability(command, result)
			s.signalStreamAvailability(command, result)
		}
		if session.auth != nil && !monitorScriptCommand(command) { s.feedMonitor(session.auth.client, command, result) }
		var slowlogClient *clientSession
		if session.auth != nil {
			slowlogClient = session.auth.client
		}
		s.recordSlowlogForClient(slowlogClient, command, time.Since(commandStarted))
		results = append(results, result)
		// Preserve Redis WATCH semantics for other clients even if a later
		// command in this same transaction restores the previous value.
		s.refreshWatchesLocked()
	}

	if atomicTx {
		// Wake blocked clients only once the transaction is known to commit.
		for index, command := range commands {
			s.signalListAvailability(command, results[index])
			s.signalZSetAvailability(command, results[index])
			s.signalStreamAvailability(command, results[index])
		}
	}

	if writes && (s.journal != nil || replicate) && !s.durabilityFailed {
		after := exportAfter()
		changes := persistenceDiff(before, after)
		if len(changes) > 0 {
			if s.journal != nil {
				if err := s.journal.Append(changes); err != nil {
					s.durabilityFailed = true
					if rollbackErr := restoreBefore(); rollbackErr != nil {
						return nil, errors.New("ERR persistence and rollback failed")
					}
					return nil, errors.New("ERR persistence append failed")
				}
			}
			if replicate {
				s.publishReplication(changes)
				session.lastReplicationOffset = s.replication.currentOffset()
			}
			if journal, ok := s.journal.(durabilityJournal); ok {
				appended, _, _ := journal.DurabilitySnapshot()
				session.lastDurabilitySequence = appended
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
		atomicTx := false
		switch {
		case len(args) == 1:
		case len(args) == 2 && strings.EqualFold(string(args[1]), "ATOMIC"):
			atomicTx = true
		default:
			return true, nil, errors.New("ERR wrong number of arguments for 'multi' command")
		}
		if session.multi {
			return true, nil, errors.New("ERR MULTI calls can not be nested")
		}
		session.multi = true
		session.atomic = atomicTx
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
	if atomic.LoadUint32(&s.metricsEnabled) == 0 {
		return
	}
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
	var started time.Time
	if atomic.LoadUint32(&s.metricsEnabled) != 0 {
		started = time.Now()
	}
	slowlogStarted := time.Now()
	handled, response, err = session.handleCommand(args)
	if handled {
		s.observeTransactionCommand(args, started, err)
		if len(args) > 0 {
			if _, isControl := transactionCommands[strings.ToUpper(string(args[0]))]; isControl {
				var slowlogClient *clientSession
				if session.auth != nil {
					slowlogClient = session.auth.client
				}
				s.recordSlowlogForClient(slowlogClient, args, time.Since(slowlogStarted))
			}
		}
	}
	return handled, response, err
}