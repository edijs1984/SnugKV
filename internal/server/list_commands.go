package server

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var listCommands = map[string]commandInfo{
	"LPUSH":  {3, 0, 1, 1, 1, true},
	"RPUSH":  {3, 0, 1, 1, 1, true},
	"LPOP":   {2, 3, 1, 1, 1, true},
	"RPOP":   {2, 3, 1, 1, 1, true},
	"LLEN":   {2, 2, 1, 1, 1, false},
	"LINDEX": {3, 3, 1, 1, 1, false},
	"LRANGE": {4, 4, 1, 1, 1, false},
}

func init() {
	for name, info := range listCommands {
		commandTable[name] = info
	}
}

func isListCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := listCommands[strings.ToUpper(string(args[0]))]
	return ok
}

func (s *Server) executeList(args [][]byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("ERR empty command")
	}
	cmd := strings.ToUpper(string(args[0]))
	info, ok := listCommands[cmd]
	if !ok {
		return nil, fmt.Errorf("ERR unknown command '%s'", cmd)
	}
	if len(args) < info.min || info.max > 0 && len(args) > info.max {
		return nil, fmt.Errorf("ERR wrong number of arguments for '%s' command", strings.ToLower(cmd))
	}

	key := string(args[1])
	switch cmd {
	case "LPUSH":
		length, err := s.store.ListPushLeft(key, args[2:])
		if err != nil {
			return nil, err
		}
		return integer(length), nil

	case "RPUSH":
		length, err := s.store.ListPushRight(key, args[2:])
		if err != nil {
			return nil, err
		}
		return integer(length), nil

	case "LPOP", "RPOP":
		count := 1
		withCount := len(args) == 3
		if withCount {
			n, err := strconv.ParseInt(string(args[2]), 10, 64)
			if err != nil || n < 0 || uint64(n) > uint64(^uint(0)>>1) {
				return nil, errors.New("ERR value is out of range, must be positive")
			}
			count = int(n)
		}

		var elements [][]byte
		var err error
		if cmd == "LPOP" {
			elements, err = s.store.ListPopLeft(key, count)
		} else {
			elements, err = s.store.ListPopRight(key, count)
		}
		if err != nil {
			return nil, err
		}
		if !withCount {
			if len(elements) == 0 {
				return nullBulk(), nil
			}
			return formatBulkString(elements[0]), nil
		}
		return listElementsResponse(elements), nil

	case "LLEN":
		length, err := s.store.ListLen(key)
		if err != nil {
			return nil, err
		}
		return integer(length), nil

	case "LINDEX":
		index, err := strconv.ParseInt(string(args[2]), 10, 64)
		if err != nil {
			return nil, errors.New("ERR value is not an integer or out of range")
		}
		value, found, err := s.store.ListIndex(key, index)
		if err != nil {
			return nil, err
		}
		return optionalBulk(value, found), nil

	case "LRANGE":
		start, err := strconv.ParseInt(string(args[2]), 10, 64)
		if err != nil {
			return nil, errors.New("ERR value is not an integer or out of range")
		}
		stop, err := strconv.ParseInt(string(args[3]), 10, 64)
		if err != nil {
			return nil, errors.New("ERR value is not an integer or out of range")
		}
		elements, err := s.store.ListRange(key, start, stop)
		if err != nil {
			return nil, err
		}
		return listElementsResponse(elements), nil
	}

	return nil, fmt.Errorf("ERR unknown command '%s'", cmd)
}

func listElementsResponse(elements [][]byte) []byte {
	items := make([][]byte, 0, len(elements))
	for _, element := range elements {
		items = append(items, formatBulkString(element))
	}
	return array(items...)
}
