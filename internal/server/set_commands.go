package server

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var setCommands = map[string]commandInfo{
	"SADD":       {3, 0, 1, 1, 1, true},
	"SREM":       {3, 0, 1, 1, 1, true},
	"SISMEMBER":  {3, 3, 1, 1, 1, false},
	"SMISMEMBER": {3, 0, 1, 1, 1, false},
	"SCARD":      {2, 2, 1, 1, 1, false},
	"SMEMBERS":   {2, 2, 1, 1, 1, false},
	"SSCAN":      {3, 0, 1, 1, 1, false},
}

func init() {
	for name, info := range setCommands {
		commandTable[name] = info
	}
}

func isSetCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := setCommands[strings.ToUpper(string(args[0]))]
	return ok
}

// executeRoutedCommand is the datatype-aware dispatcher used by the pressure
// layer. HASH routing remains in executeCommand; SET routing is layered here so
// new native datatypes do not expand the monolithic server switch.
func (s *Server) executeRoutedCommand(args [][]byte) ([]byte, error) {
	if len(args) > 0 {
		cmd := strings.ToUpper(string(args[0]))
		if (cmd == "RENAME" || cmd == "RENAMENX") && len(args) == 3 {
			handled, renamed, err := s.store.RenameSet(string(args[1]), string(args[2]), cmd == "RENAMENX")
			if handled {
				if err != nil {
					return nil, err
				}
				if cmd == "RENAMENX" {
					return boolean(renamed), nil
				}
				return []byte("+OK\r\n"), nil
			}
		}
	}
	if isSetCommand(args) {
		return s.executeSet(args)
	}
	return s.executeCommand(args)
}

func (s *Server) executeSet(args [][]byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("ERR empty command")
	}
	cmd := strings.ToUpper(string(args[0]))
	info, ok := setCommands[cmd]
	if !ok {
		return nil, fmt.Errorf("ERR unknown command '%s'", cmd)
	}
	if len(args) < info.min || info.max > 0 && len(args) > info.max {
		return nil, fmt.Errorf("ERR wrong number of arguments for '%s' command", strings.ToLower(cmd))
	}

	key := string(args[1])
	switch cmd {
	case "SADD":
		added, err := s.store.SetAdd(key, args[2:])
		if err != nil {
			return nil, err
		}
		return integer(added), nil

	case "SREM":
		removed, err := s.store.SetRemove(key, args[2:])
		if err != nil {
			return nil, err
		}
		return integer(removed), nil

	case "SISMEMBER":
		found, err := s.store.SetContains(key, args[2])
		if err != nil {
			return nil, err
		}
		return boolean(found), nil

	case "SMISMEMBER":
		found, err := s.store.SetMultiContains(key, args[2:])
		if err != nil {
			return nil, err
		}
		items := make([][]byte, 0, len(found))
		for _, present := range found {
			if present {
				items = append(items, integer(1))
			} else {
				items = append(items, integer(0))
			}
		}
		return array(items...), nil

	case "SCARD":
		count, err := s.store.SetLen(key)
		if err != nil {
			return nil, err
		}
		return integer(count), nil

	case "SMEMBERS":
		members, err := s.store.SetMembers(key)
		if err != nil {
			return nil, err
		}
		items := make([][]byte, 0, len(members))
		for _, member := range members {
			items = append(items, formatBulkString(member))
		}
		return array(items...), nil

	case "SSCAN":
		cursor, err := strconv.ParseUint(string(args[2]), 10, 64)
		if err != nil {
			return nil, errors.New("ERR invalid cursor")
		}

		count := 10
		var pattern []byte
		for i := 3; i < len(args); {
			switch strings.ToUpper(string(args[i])) {
			case "MATCH":
				if i+1 >= len(args) {
					return nil, errors.New("ERR syntax error")
				}
				pattern = args[i+1]
				i += 2

			case "COUNT":
				if i+1 >= len(args) {
					return nil, errors.New("ERR syntax error")
				}
				n, err := strconv.Atoi(string(args[i+1]))
				if err != nil || n <= 0 {
					return nil, errors.New("ERR syntax error")
				}
				if n > 10000 {
					n = 10000
				}
				count = n
				i += 2

			default:
				return nil, errors.New("ERR syntax error")
			}
		}

		next, members, err := s.store.SetScan(key, cursor, count, pattern)
		if err != nil {
			return nil, err
		}
		items := make([][]byte, 0, len(members))
		for _, member := range members {
			items = append(items, formatBulkString(member))
		}
		return array(
			formatBulkString([]byte(strconv.FormatUint(next, 10))),
			array(items...),
		), nil
	}

	return nil, fmt.Errorf("ERR unknown command '%s'", cmd)
}
