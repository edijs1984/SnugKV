package server

import (
	"errors"
	"strconv"
	"strings"
)

var copyCommands = map[string]commandInfo{
	"COPY": {3, 0, 1, 2, 1, true},
}

func init() {
	for name, info := range copyCommands {
		commandTable[name] = info
	}
}

func isCopyCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := copyCommands[strings.ToUpper(string(args[0]))]
	return ok
}

type copyOptions struct {
	replace bool
}

func parseCopyOptions(args [][]byte) (copyOptions, error) {
	var options copyOptions
	for i := 0; i < len(args); {
		switch strings.ToUpper(string(args[i])) {
		case "REPLACE":
			options.replace = true
			i++
		case "DB":
			if i+1 >= len(args) {
				return copyOptions{}, errors.New("ERR syntax error")
			}
			db, err := strconv.ParseInt(string(args[i+1]), 10, 64)
			if err != nil {
				return copyOptions{}, errors.New("ERR value is not an integer or out of range")
			}
			// SnugKV intentionally exposes only database 0. Accepting DB 0 keeps
			// the Redis COPY grammar useful without pretending that additional
			// logical databases exist.
			if db != 0 {
				return copyOptions{}, errors.New("ERR DB index is out of range")
			}
			i += 2
		default:
			return copyOptions{}, errors.New("ERR syntax error")
		}
	}
	return options, nil
}

func (s *Server) executeCopy(args [][]byte) ([]byte, error) {
	if len(args) < 3 {
		return nil, errors.New("ERR wrong number of arguments for 'copy' command")
	}
	options, err := parseCopyOptions(args[3:])
	if err != nil {
		return nil, err
	}

	source := string(args[1])
	destination := string(args[2])
	if source == destination {
		return nil, errors.New("ERR source and destination objects are the same")
	}

	records := s.store.Export([]string{source})
	if len(records) != 1 || records[0].Deleted {
		return integer(0), nil
	}
	if !options.replace && s.store.Exists([]string{destination}) != 0 {
		return integer(0), nil
	}

	// Export is the canonical logical representation used by persistence. By
	// changing only the key before Restore, COPY preserves the source datatype,
	// value, stream/group metadata, and absolute expiry while allocating fully
	// independent destination storage.
	records[0].Key = []byte(destination)
	if err := s.store.Restore(records, false); err != nil {
		return nil, err
	}
	return integer(1), nil
}
