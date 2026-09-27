package server

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"snugkv/internal/persistence"
)

type slowlogEntry struct {
	id        int64
	timestamp int64
	duration  int64
	args      [][]byte
}

func (s *Server) recordSlowlog(args [][]byte, elapsed time.Duration) {
	if len(args) == 0 {
		return
	}
	duration := elapsed.Microseconds()

	s.slowlogMu.Lock()
	defer s.slowlogMu.Unlock()

	if s.slowlogThresholdMicros < 0 || duration < s.slowlogThresholdMicros {
		return
	}
	entry := slowlogEntry{
		id:        s.slowlogNextID,
		timestamp: time.Now().Unix(),
		duration:  duration,
		args:      cloneCommandArgs(args),
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
				formatBulkString(nil),
				formatBulkString(nil),
			))
		}
		return array(items...), nil

	default:
		return nil, fmt.Errorf("ERR unknown subcommand '%s'. Try SLOWLOG HELP.", args[1])
	}
}

func (s *Server) snapshotNow() error {
	if s.snapshotPath == "" {
		return errors.New("ERR snapshot persistence is disabled")
	}
	if err := persistence.Snapshot(s.snapshotPath, s.store.Export(nil)); err != nil {
		return errors.New("ERR snapshot save failed")
	}
	s.lastSaveUnix.Store(time.Now().Unix())
	return nil
}

func (s *Server) executeSave(background bool, schedule bool) ([]byte, error) {
	if !background {
		if err := s.snapshotNow(); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil
	}

	s.persistenceJobMu.Lock()
	if s.bgsaveRunning {
		s.persistenceJobMu.Unlock()
		if schedule {
			return []byte("+Background saving scheduled\r\n"), nil
		}
		return nil, errors.New("ERR Background save already in progress")
	}
	s.bgsaveRunning = true
	s.persistenceJobMu.Unlock()

	go func() {
		_ = s.snapshotNow()
		s.persistenceJobMu.Lock()
		s.bgsaveRunning = false
		s.persistenceJobMu.Unlock()
	}()

	return []byte("+Background saving started\r\n"), nil
}

func (s *Server) executeBGSAVE(args [][]byte) ([]byte, error) {
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
	if !ok {
		return nil, errors.New("ERR AOF is disabled")
	}

	s.persistenceJobMu.Lock()
	if s.aofRewriteRunning {
		s.persistenceJobMu.Unlock()
		return nil, errors.New("ERR Background append only file rewriting already in progress")
	}
	s.aofRewriteRunning = true
	s.persistenceJobMu.Unlock()

	records := s.store.Export(nil)
	go func() {
		_ = writer.Rewrite(records)
		s.persistenceJobMu.Lock()
		s.aofRewriteRunning = false
		s.persistenceJobMu.Unlock()
	}()

	return []byte("+Background append only file rewriting started\r\n"), nil
}
