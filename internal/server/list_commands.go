package server

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var listCommands = map[string]commandInfo{
	"LPUSH":   {3, 0, 1, 1, 1, true},
	"RPUSH":   {3, 0, 1, 1, 1, true},
	"LPUSHX":  {3, 0, 1, 1, 1, true},
	"RPUSHX":  {3, 0, 1, 1, 1, true},
	"LPOP":    {2, 3, 1, 1, 1, true},
	"RPOP":    {2, 3, 1, 1, 1, true},
	"LLEN":    {2, 2, 1, 1, 1, false},
	"LINDEX":  {3, 3, 1, 1, 1, false},
	"LRANGE":  {4, 4, 1, 1, 1, false},
	"LSET":    {4, 4, 1, 1, 1, true},
	"LTRIM":   {4, 4, 1, 1, 1, true},
	"LREM":    {4, 4, 1, 1, 1, true},
	"LINSERT": {5, 5, 1, 1, 1, true},
	"LPOS":    {3, 0, 1, 1, 1, false},
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
	case "LPUSH", "LPUSHX":
		var length int64
		var err error
		if cmd == "LPUSH" {
			length, err = s.store.ListPushLeft(key, args[2:])
		} else {
			length, err = s.store.ListPushLeftX(key, args[2:])
		}
		if err != nil {
			return nil, err
		}
		return integer(length), nil

	case "RPUSH", "RPUSHX":
		var length int64
		var err error
		if cmd == "RPUSH" {
			length, err = s.store.ListPushRight(key, args[2:])
		} else {
			length, err = s.store.ListPushRightX(key, args[2:])
		}
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

	case "LSET":
		index, err := strconv.ParseInt(string(args[2]), 10, 64)
		if err != nil {
			return nil, errors.New("ERR value is not an integer or out of range")
		}
		if err := s.store.ListSet(key, index, args[3]); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil

	case "LTRIM":
		start, err := strconv.ParseInt(string(args[2]), 10, 64)
		if err != nil {
			return nil, errors.New("ERR value is not an integer or out of range")
		}
		stop, err := strconv.ParseInt(string(args[3]), 10, 64)
		if err != nil {
			return nil, errors.New("ERR value is not an integer or out of range")
		}
		if err := s.store.ListTrim(key, start, stop); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil

	case "LREM":
		count, err := strconv.ParseInt(string(args[2]), 10, 64)
		if err != nil {
			return nil, errors.New("ERR value is not an integer or out of range")
		}
		removed, err := s.store.ListRemove(key, count, args[3])
		if err != nil {
			return nil, err
		}
		return integer(removed), nil

	case "LINSERT":
		position := strings.ToUpper(string(args[2]))
		if position != "BEFORE" && position != "AFTER" {
			return nil, errors.New("ERR syntax error")
		}
		length, err := s.store.ListInsert(key, position == "BEFORE", args[3], args[4])
		if err != nil {
			return nil, err
		}
		return integer(length), nil

	case "LPOS":
		rank := int64(1)
		count := int64(1)
		maxLen := int64(0)
		withCount := false

		for i := 3; i < len(args); {
			switch strings.ToUpper(string(args[i])) {
			case "RANK":
				if i+1 >= len(args) {
					return nil, errors.New("ERR syntax error")
				}
				n, err := strconv.ParseInt(string(args[i+1]), 10, 64)
				if err != nil {
					return nil, errors.New("ERR value is not an integer or out of range")
				}
				if n == 0 {
					return nil, errors.New("ERR RANK can't be zero")
				}
				rank = n
				i += 2

			case "COUNT":
				if i+1 >= len(args) {
					return nil, errors.New("ERR syntax error")
				}
				n, err := strconv.ParseInt(string(args[i+1]), 10, 64)
				if err != nil || n < 0 {
					return nil, errors.New("ERR COUNT can't be negative")
				}
				count = n
				withCount = true
				i += 2

			case "MAXLEN":
				if i+1 >= len(args) {
					return nil, errors.New("ERR syntax error")
				}
				n, err := strconv.ParseInt(string(args[i+1]), 10, 64)
				if err != nil || n < 0 {
					return nil, errors.New("ERR MAXLEN can't be negative")
				}
				maxLen = n
				i += 2

			default:
				return nil, errors.New("ERR syntax error")
			}
		}

		positions, err := s.store.ListPos(key, args[2], rank, count, maxLen, withCount)
		if err != nil {
			return nil, err
		}
		if !withCount {
			if len(positions) == 0 {
				return nullBulk(), nil
			}
			return integer(positions[0]), nil
		}
		items := make([][]byte, 0, len(positions))
		for _, position := range positions {
			items = append(items, integer(position))
		}
		return array(items...), nil
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
