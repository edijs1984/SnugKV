package server

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"snugkv/internal/persistence"
)

const (
	slowlogEntryMaxArgc      = 32
	slowlogEntryMaxStringLen = 128
)

func slowlogSanitizeArgs(args [][]byte) [][]byte {
	if len(args) == 0 {
		return nil
	}

	limit := len(args)
	if limit > slowlogEntryMaxArgc {
		limit = slowlogEntryMaxArgc
	}
	out := make([][]byte, limit)
	for i := 0; i < limit; i++ {
		if len(args) > slowlogEntryMaxArgc && i == limit-1 {
			out[i] = []byte(fmt.Sprintf("... (%d more arguments)", len(args)-slowlogEntryMaxArgc+1))
			continue
		}
		arg := args[i]
		if len(arg) > slowlogEntryMaxStringLen {
			trimmed := make([]byte, 0, slowlogEntryMaxStringLen+32)
			trimmed = append(trimmed, arg[:slowlogEntryMaxStringLen]...)
			trimmed = append(trimmed, []byte(fmt.Sprintf("... (%d more bytes)", len(arg)-slowlogEntryMaxStringLen))...)
			out[i] = trimmed
			continue
		}
		out[i] = append([]byte(nil), arg...)
	}

	slowlogRedactSensitiveArgs(out)
	return out
}

func slowlogRedactSensitiveArgs(args [][]byte) {
	if len(args) == 0 {
		return
	}
	redacted := []byte("(redacted)")
	cmd := strings.ToUpper(string(args[0]))
	switch cmd {
	case "ACL":
		if len(args) < 2 {
			return
		}
		sub := strings.ToUpper(string(args[1]))
		switch sub {
		case "SETUSER", "GETUSER", "DELUSER":
			for i := 2; i < len(args); i++ {
				args[i] = append([]byte(nil), redacted...)
			}
		}
	case "MIGRATE":
		for i := 1; i < len(args); i++ {
			switch strings.ToUpper(string(args[i])) {
			case "AUTH":
				if i+1 < len(args) {
					args[i+1] = append([]byte(nil), redacted...)
					i++
				}
			case "AUTH2":
				if i+1 < len(args) {
					args[i+1] = append([]byte(nil), redacted...)
				}
				if i+2 < len(args) {
					args[i+2] = append([]byte(nil), redacted...)
					i += 2
				}
			}
		}
	}
}

type slowlogEntry struct {
	id        int64
	timestamp int64
	duration  int64
	args      [][]byte
	peer      string
	name      string
}

func (s *Server) recordSlowlog(args [][]byte, elapsed time.Duration) {
	s.recordSlowlogForClient(s.executionClient, args, elapsed)
}

func (s *Server) recordSlowlogForClient(client *clientSession, args [][]byte, elapsed time.Duration) {
	if len(args) == 0 {
		return
	}
	duration := elapsed.Microseconds()
	var peer, name string
	if client != nil {
		client.mu.RLock()
		peer, name = client.remoteAddr, client.name
		client.mu.RUnlock()
	}

	s.slowlogMu.Lock()
	defer s.slowlogMu.Unlock()

	if s.slowlogThresholdMicros < 0 || duration < s.slowlogThresholdMicros {
		return
	}
	entry := slowlogEntry{
		id:        s.slowlogNextID,
		timestamp: time.Now().Unix(),
		duration:  duration,
		args:      slowlogSanitizeArgs(args),
		peer:      peer,
		name:      name,
	}
	s.slowlogNextID++
	s.slowlogEntries = append([]slowlogEntry{entry}, s.slowlogEntries...)
	if s.slowlogMaxLen >= 0 && len(s.slowlogEntries) > s.slowlogMaxLen {
		s.slowlogEntries = s.slowlogEntries[:s.slowlogMaxLen]
	}
}

func (s *Server) executeSlowlog(args [][]byte) ([]byte, error) {
	if len(args) < 2 {
		return nil, errors.New("ERR wrong number of arguments for 'slowlog' command")
	}
	sub := strings.ToUpper(string(args[1]))

	switch sub {
	case "HELP":
		if len(args) != 2 {
			return nil, errors.New("ERR wrong number of arguments for 'slowlog|help' command")
		}
		return array(
			formatBulkString([]byte("SLOWLOG <subcommand> [<arg> [value] [opt] ...]. Subcommands are:")),
			formatBulkString([]byte("GET [<count>]")),
			formatBulkString([]byte("    Return top <count> entries from the slowlog (default: 10, -1 mean all).")),
			formatBulkString([]byte("    Entries are made of:")),
			formatBulkString([]byte("    id, timestamp, time in microseconds, arguments array, client IP and port,")),
			formatBulkString([]byte("    client name")),
			formatBulkString([]byte("LEN")),
			formatBulkString([]byte("    Return the length of the slowlog.")),
			formatBulkString([]byte("RESET")),
			formatBulkString([]byte("    Reset the slowlog.")),
			formatBulkString([]byte("HELP")),
			formatBulkString([]byte("    Print this help.")),
		), nil

	case "LEN":
		if len(args) != 2 {
			return nil, errors.New("ERR wrong number of arguments for 'slowlog|len' command")
		}
		s.slowlogMu.Lock()
		n := len(s.slowlogEntries)
		s.slowlogMu.Unlock()
		return integer(int64(n)), nil

	case "RESET":
		if len(args) != 2 {
			return nil, errors.New("ERR wrong number of arguments for 'slowlog|reset' command")
		}
		s.slowlogMu.Lock()
		s.slowlogEntries = nil
		s.slowlogMu.Unlock()
		return []byte("+OK\r\n"), nil

	case "GET":
		if len(args) > 3 {
			return nil, fmt.Errorf("ERR unknown subcommand or wrong number of arguments for '%s'. Try SLOWLOG HELP.", args[1])
		}
		count := int64(10)
		if len(args) == 3 {
			n, err := strconv.ParseInt(string(args[2]), 10, 64)
			if err != nil || n < -1 || strconv.FormatInt(n, 10) != string(args[2]) {
				return nil, errors.New("ERR count should be greater than or equal to -1")
			}
			count = n
		}

		s.slowlogMu.Lock()
		if count == -1 || count > int64(len(s.slowlogEntries)) {
			count = int64(len(s.slowlogEntries))
		}
		entries := append([]slowlogEntry(nil), s.slowlogEntries[:count]...)
		s.slowlogMu.Unlock()

		items := make([][]byte, 0, len(entries))
		for _, entry := range entries {
			command := make([][]byte, 0, len(entry.args))
			for _, arg := range entry.args {
				command = append(command, formatBulkString(arg))
			}
			items = append(items, array(
				integer(entry.id),
				integer(entry.timestamp),
				integer(entry.duration),
				array(command...),
				formatBulkString([]byte(entry.peer)),
				formatBulkString([]byte(entry.name)),
			))
		}
		return array(items...), nil

	default:
		return nil, fmt.Errorf("ERR unknown subcommand '%s'. Try SLOWLOG HELP.", args[1])
	}
}

func (s *Server) startPersistenceJob(run func()) {
	s.persistenceJobs.Add(1)
	go func() {
		defer s.persistenceJobs.Done()
		run()
	}()
}

func (s *Server) waitPersistenceJobs() {
	s.persistenceJobs.Wait()
}

func (s *Server) snapshotRecords(records []persistence.Record) (resultErr error) {
	defer func() {
		s.persistenceJobMu.Lock()
		s.rdbLastSaveFailed = resultErr != nil
		s.persistenceJobMu.Unlock()
		if resultErr != nil {
			log.Printf("event=snapshot_save_failed error=%q", resultErr)
		}
	}()
	if s.snapshotPath == "" {
		return errors.New("ERR snapshot persistence is disabled")
	}
	if err := persistence.Snapshot(s.snapshotPath, records); err != nil {
		return fmt.Errorf("ERR snapshot save failed: %w", err)
	}
	s.lastSaveUnix.Store(time.Now().Unix())
	return nil
}

func (s *Server) snapshotNow() error {
	return s.snapshotRecords(s.store.Export(nil))
}

func (s *Server) executeSave(background bool, schedule bool) ([]byte, error) {
	if !background {
		s.persistenceJobMu.Lock()
		if s.bgsaveRunning {
			s.persistenceJobMu.Unlock()
			return nil, errors.New("ERR Background save already in progress")
		}
		if s.saveRunning {
			s.persistenceJobMu.Unlock()
			return nil, errors.New("ERR Background save already in progress")
		}
		s.saveRunning = true
		s.persistenceJobMu.Unlock()

		err := s.snapshotNow()

		s.persistenceJobMu.Lock()
		s.saveRunning = false
		s.persistenceJobMu.Unlock()

		if err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil
	}

	s.persistenceJobMu.Lock()
	if s.saveRunning || s.bgsaveRunning {
		s.persistenceJobMu.Unlock()
		return nil, errors.New("ERR Background save already in progress")
	}
	if s.aofRewriteRunning {
		if schedule {
			s.bgsaveScheduled = true
			s.persistenceJobMu.Unlock()
			return []byte("+Background saving scheduled\r\n"), nil
		}
		s.persistenceJobMu.Unlock()
		return nil, errors.New("ERR Another child process is active (AOF?): can't BGSAVE right now. Use BGSAVE SCHEDULE in order to schedule a BGSAVE whenever possible.")
	}
	s.bgsaveRunning = true
	s.persistenceJobMu.Unlock()

	records := s.store.Export(nil)
	s.startPersistenceJob(func() {
		s.runBackgroundSaveRecords(records)
	})

	return []byte("+Background saving started\r\n"), nil
}

func (s *Server) executeBGSAVE(args [][]byte) ([]byte, error) {
	if len(args) > 2 {
		return nil, errors.New("ERR syntax error")
	}
	schedule := false
	if len(args) == 2 {
		if !strings.EqualFold(string(args[1]), "SCHEDULE") {
			return nil, errors.New("ERR syntax error")
		}
		schedule = true
	}
	return s.executeSave(true, schedule)
}

func (s *Server) executeBGRewriteAOF() ([]byte, error) {
	writer, ok := s.journal.(interface {
		Rewrite([]persistence.Record) error
	})
	if !ok && s.aofRewritePath == "" {
		return nil, errors.New("ERR AOF is disabled")
	}

	s.persistenceJobMu.Lock()
	if s.aofRewriteRunning {
		s.persistenceJobMu.Unlock()
		return nil, errors.New("ERR Background append only file rewriting already in progress")
	}
	if s.bgsaveRunning {
		s.aofRewriteScheduled = true
		s.persistenceJobMu.Unlock()
		return []byte("+Background append only file rewriting scheduled\r\n"), nil
	}
	s.aofRewriteScheduled = false
	// Open only a temporary writer: never install it as the active journal.
	// Open holds the persistence file lock until Close, including during Rewrite.
	var temporary *persistence.Log
	if !ok {
		var err error
		temporary, err = persistence.Open(s.aofRewritePath, "no")
		if err != nil {
			s.aofLastRewriteFailed = true
			s.persistenceJobMu.Unlock()
			return nil, errors.New("ERR cannot open AOF rewrite destination")
		}
		writer = temporary
	}
	s.aofRewriteRunning = true
	s.persistenceJobMu.Unlock()

	// An active journal must not accept writes between export and replacement.
	// Start after this command releases durableMu, then hold it across both.
	// One-off exports have no active journal and can capture immediately.
	var records []persistence.Record
	if temporary != nil {
		records = s.store.Export(nil)
	}
	s.startPersistenceJob(func() {
		if temporary == nil {
			s.durableMu.Lock()
			records = s.store.Export(nil)
		}
		rewriteErr := writer.Rewrite(records)
		if temporary == nil {
			s.durableMu.Unlock()
		}
		if temporary != nil {
			rewriteErr = errors.Join(rewriteErr, temporary.Close())
		}
		s.persistenceJobMu.Lock()
		s.aofLastRewriteFailed = rewriteErr != nil
		s.aofRewriteRunning = false
		startSave := s.bgsaveScheduled
		if startSave {
			s.bgsaveScheduled = false
			s.bgsaveRunning = true
		}
		s.persistenceJobMu.Unlock()
		if startSave {
			saveRecords := s.store.Export(nil)
			s.startPersistenceJob(func() {
				s.runBackgroundSaveRecords(saveRecords)
			})
		}
		if rewriteErr != nil {
			log.Printf("event=aof_rewrite_failed error=%q", rewriteErr)
		}
	})

	return []byte("+Background append only file rewriting started\r\n"), nil
}

func (s *Server) persistenceInfo() string {
	s.persistenceJobMu.Lock()
	saving, rewriting, rewriteScheduled := 0, 0, 0
	if s.aofRewriteScheduled {
		rewriteScheduled = 1
	}
	if s.bgsaveRunning {
		saving = 1
	}
	if s.aofRewriteRunning {
		rewriting = 1
	}
	rdbStatus, aofStatus := "ok", "ok"
	if s.rdbLastSaveFailed {
		rdbStatus = "err"
	}
	if s.aofLastRewriteFailed {
		aofStatus = "err"
	}
	s.persistenceJobMu.Unlock()
	enabled := 0
	if s.journal != nil {
		enabled = 1
	}
	return fmt.Sprintf("# Persistence\r\nrdb_bgsave_in_progress:%d\r\nrdb_last_save_time:%d\r\nrdb_last_bgsave_status:%s\r\naof_enabled:%d\r\naof_rewrite_in_progress:%d\r\naof_rewrite_scheduled:%d\r\naof_last_bgrewrite_status:%s\r\n",
		saving, s.lastSaveUnix.Load(), rdbStatus, enabled, rewriting, rewriteScheduled, aofStatus)
}

func (s *Server) runBackgroundSave() {
	s.runBackgroundSaveRecords(s.store.Export(nil))
}

func (s *Server) runBackgroundSaveRecords(records []persistence.Record) {
	_ = s.snapshotRecords(records) // snapshotRecords records and logs failures.
	// Serialize the handoff with commands so another job cannot start
	// between clearing the save flag and starting its queued rewrite.
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	s.persistenceJobMu.Lock()
	s.bgsaveRunning = false
	startRewrite := s.aofRewriteScheduled
	s.persistenceJobMu.Unlock()
	if startRewrite {
		if _, err := s.executeBGRewriteAOF(); err != nil {
			s.persistenceJobMu.Lock()
			s.aofRewriteScheduled = false
			s.aofLastRewriteFailed = true
			s.persistenceJobMu.Unlock()
			log.Printf("event=scheduled_aof_rewrite_failed error=%q", err)
		}
	}
}
