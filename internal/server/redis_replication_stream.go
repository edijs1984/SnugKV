package server

import (
	"bufio"
	"errors"
	"fmt"
	"io"
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

func (s *Server) applyRedisReplicationBatch(commands [][][]byte) error {
	if len(commands) == 0 {
		return nil
	}
	s.durableMu.Lock()
	defer s.durableMu.Unlock()

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
			return fmt.Errorf("apply Redis replication command %s: %w", cmd, err)
		}
	}
	s.refreshWatchesLocked()
	return nil
}
