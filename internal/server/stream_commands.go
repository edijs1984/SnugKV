package server

import (
	"errors"
	"fmt"
	"snugkv/internal/engine"
	"strconv"
	"strings"
)

var streamCommands = map[string]commandInfo{
	"XADD":      {5, 0, 1, 1, 1, true},
	"XLEN":      {2, 2, 1, 1, 1, false},
	"XRANGE":    {4, 6, 1, 1, 1, false},
	"XREVRANGE": {4, 6, 1, 1, 1, false},
	"XDEL":      {3, 0, 1, 1, 1, true},
	"XTRIM":     {4, 0, 1, 1, 1, true},
}

func init() {
	for name, info := range streamCommands {
		commandTable[name] = info
	}
}

func isStreamCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := streamCommands[strings.ToUpper(string(args[0]))]
	return ok
}

func parseNonNegativeInt(arg []byte) (int, error) {
	n, err := strconv.ParseInt(string(arg), 10, 64)
	if err != nil || n < 0 || int64(int(n)) != n {
		return 0, errors.New("ERR value is not an integer or out of range")
	}
	return int(n), nil
}

func parseXAdd(args [][]byte) (string, []engine.StreamField, engine.StreamAddOptions, error) {
	options := engine.StreamAddOptions{}
	i := 2
	for i < len(args) {
		switch strings.ToUpper(string(args[i])) {
		case "NOMKSTREAM":
			options.NoMkStream = true
			i++
		case "MAXLEN":
			if options.HasMaxLen {
				return "", nil, options, errors.New("ERR syntax error")
			}
			i++
			if i < len(args) && (string(args[i]) == "~" || string(args[i]) == "=") {
				i++
			}
			if i >= len(args) {
				return "", nil, options, errors.New("ERR syntax error")
			}
			maxLen, err := parseNonNegativeInt(args[i])
			if err != nil {
				return "", nil, options, err
			}
			options.HasMaxLen = true
			options.MaxLen = maxLen
			i++
			// Redis permits LIMIT with approximate trimming. SnugKV's packed
			// phase-1 implementation trims exactly, so LIMIT is accepted and
			// parsed for wire compatibility but does not weaken MAXLEN.
			if i < len(args) && strings.EqualFold(string(args[i]), "LIMIT") {
				if i+1 >= len(args) {
					return "", nil, options, errors.New("ERR syntax error")
				}
				if _, err := parseNonNegativeInt(args[i+1]); err != nil {
					return "", nil, options, err
				}
				i += 2
			}
		default:
			goto id
		}
	}

id:
	if i >= len(args) {
		return "", nil, options, errors.New("ERR syntax error")
	}
	id := string(args[i])
	i++
	if i >= len(args) || (len(args)-i)%2 != 0 {
		return "", nil, options, errors.New("ERR wrong number of arguments for 'xadd' command")
	}
	fields := make([]engine.StreamField, 0, (len(args)-i)/2)
	for ; i < len(args); i += 2 {
		fields = append(fields, engine.StreamField{
			Field: append([]byte(nil), args[i]...),
			Value: append([]byte(nil), args[i+1]...),
		})
	}
	return id, fields, options, nil
}

func parseStreamCount(args [][]byte, start int) (int, error) {
	count := int(^uint(0) >> 1)
	if start == len(args) {
		return count, nil
	}
	if start+2 != len(args) || !strings.EqualFold(string(args[start]), "COUNT") {
		return 0, errors.New("ERR syntax error")
	}
	n, err := strconv.ParseInt(string(args[start+1]), 10, 64)
	if err != nil || n <= 0 || int64(int(n)) != n {
		return 0, errors.New("ERR value is not an integer or out of range")
	}
	return int(n), nil
}

func streamEntriesResponse(entries []engine.StreamEntry) []byte {
	response := make([][]byte, 0, len(entries))
	for _, item := range entries {
		fields := make([][]byte, 0, len(item.Fields)*2)
		for _, pair := range item.Fields {
			fields = append(fields, formatBulkString(pair.Field), formatBulkString(pair.Value))
		}
		response = append(response, array(
			formatBulkString([]byte(item.ID.String())),
			array(fields...),
		))
	}
	return array(response...)
}

func (s *Server) executeStream(args [][]byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("ERR empty command")
	}
	cmd := strings.ToUpper(string(args[0]))
	info, ok := streamCommands[cmd]
	if !ok {
		return nil, fmt.Errorf("ERR unknown command '%s'", cmd)
	}
	if len(args) < info.min || info.max > 0 && len(args) > info.max {
		return nil, fmt.Errorf("ERR wrong number of arguments for '%s' command", strings.ToLower(cmd))
	}
	key := string(args[1])

	switch cmd {
	case "XADD":
		idSpec, fields, options, err := parseXAdd(args)
		if err != nil {
			return nil, err
		}
		id, applied, err := s.store.StreamAdd(key, idSpec, fields, options)
		if err != nil {
			return nil, err
		}
		if !applied {
			return nullBulk(), nil
		}
		return formatBulkString([]byte(id.String())), nil

	case "XLEN":
		length, err := s.store.StreamLen(key)
		if err != nil {
			return nil, err
		}
		return integer(length), nil

	case "XRANGE", "XREVRANGE":
		reverse := cmd == "XREVRANGE"
		var startArg, endArg string
		if reverse {
			endArg = string(args[2])
			startArg = string(args[3])
		} else {
			startArg = string(args[2])
			endArg = string(args[3])
		}
		start, err := engine.ParseStreamRangeBound(startArg, true)
		if err != nil {
			return nil, err
		}
		end, err := engine.ParseStreamRangeBound(endArg, false)
		if err != nil {
			return nil, err
		}
		count, err := parseStreamCount(args, 4)
		if err != nil {
			return nil, err
		}
		entries, err := s.store.StreamRange(key, start, end, count, reverse)
		if err != nil {
			return nil, err
		}
		return streamEntriesResponse(entries), nil

	case "XDEL":
		ids := make([]engine.StreamID, 0, len(args)-2)
		for _, arg := range args[2:] {
			id, err := engine.ParseStreamID(string(arg))
			if err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		deleted, err := s.store.StreamDelete(key, ids)
		if err != nil {
			return nil, err
		}
		return integer(deleted), nil

	case "XTRIM":
		if !strings.EqualFold(string(args[2]), "MAXLEN") {
			return nil, errors.New("ERR syntax error")
		}
		i := 3
		if i < len(args) && (string(args[i]) == "~" || string(args[i]) == "=") {
			i++
		}
		if i >= len(args) {
			return nil, errors.New("ERR syntax error")
		}
		maxLen, err := parseNonNegativeInt(args[i])
		if err != nil {
			return nil, err
		}
		i++
		limit := 0
		if i < len(args) {
			if i+2 != len(args) || !strings.EqualFold(string(args[i]), "LIMIT") {
				return nil, errors.New("ERR syntax error")
			}
			limit, err = parseNonNegativeInt(args[i+1])
			if err != nil {
				return nil, err
			}
			i += 2
		}
		if i != len(args) {
			return nil, errors.New("ERR syntax error")
		}
		trimmed, err := s.store.StreamTrimMaxLen(key, maxLen, limit)
		if err != nil {
			return nil, err
		}
		return integer(trimmed), nil
	}
	return nil, errors.New("ERR unsupported stream command")
}
