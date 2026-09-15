package server

import (
	"errors"
	"fmt"
	"snugkv/internal/engine"
	"strconv"
	"strings"
)

var zsetOpsCommands = map[string]commandInfo{
	"ZPOPMIN":     {2, 3, 1, 1, 1, true},
	"ZPOPMAX":     {2, 3, 1, 1, 1, true},
	"ZMPOP":       {5, 0, 0, 0, 0, true},
	"ZMSCORE":     {3, 0, 1, 1, 1, false},
	"ZRANDMEMBER": {2, 4, 1, 1, 1, false},
	"ZSCAN":       {3, 0, 1, 1, 1, false},
	"ZRANGESTORE": {5, 0, 1, 1, 1, true},
}

func init() {
	for name, info := range zsetOpsCommands {
		commandTable[name] = info
	}
}

func isZSetOpsCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := zsetOpsCommands[strings.ToUpper(string(args[0]))]
	return ok
}

func zsetMPopKeys(args [][]byte) []string {
	if len(args) < 3 {
		return nil
	}
	n, err := strconv.Atoi(string(args[1]))
	if err != nil || n <= 0 || 2+n > len(args) {
		return nil
	}
	keys := make([]string, 0, n)
	for i := 0; i < n; i++ {
		keys = append(keys, string(args[2+i]))
	}
	return keys
}

func zsetOpsPressureKeys(args [][]byte) []string {
	if len(args) == 0 {
		return nil
	}
	switch strings.ToUpper(string(args[0])) {
	case "ZMPOP":
		return zsetMPopKeys(args)
	case "ZRANGESTORE":
		if len(args) >= 3 {
			return []string{string(args[1]), string(args[2])}
		}
	}
	return nil
}

func executeZSetPopResponse(items []engine.ZSetItem) []byte {
	return zsetItemsResponse(items, true)
}

func (s *Server) executeZSetOps(args [][]byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("ERR empty command")
	}
	cmd := strings.ToUpper(string(args[0]))
	info, ok := zsetOpsCommands[cmd]
	if !ok {
		return nil, fmt.Errorf("ERR unknown command '%s'", cmd)
	}
	if len(args) < info.min || info.max > 0 && len(args) > info.max {
		return nil, fmt.Errorf("ERR wrong number of arguments for '%s' command", strings.ToLower(cmd))
	}

	switch cmd {
	case "ZPOPMIN", "ZPOPMAX":
		count := int64(1)
		if len(args) == 3 {
			var err error
			count, err = strconv.ParseInt(string(args[2]), 10, 64)
			if err != nil {
				return nil, errors.New("ERR value is out of range, must be positive")
			}
		}
		items, err := s.store.ZSetPop(string(args[1]), count, cmd == "ZPOPMAX")
		if err != nil {
			return nil, err
		}
		return executeZSetPopResponse(items), nil

	case "ZMPOP":
		numKeys, err := strconv.Atoi(string(args[1]))
		if err != nil || numKeys <= 0 {
			return nil, errors.New("ERR numkeys should be greater than 0")
		}
		sideIndex := 2 + numKeys
		if sideIndex >= len(args) {
			return nil, errors.New("ERR syntax error")
		}
		keys := make([]string, numKeys)
		for i := 0; i < numKeys; i++ {
			keys[i] = string(args[2+i])
		}
		side := strings.ToUpper(string(args[sideIndex]))
		if side != "MIN" && side != "MAX" {
			return nil, errors.New("ERR syntax error")
		}
		count := int64(1)
		if sideIndex+1 < len(args) {
			if sideIndex+3 != len(args) || !strings.EqualFold(string(args[sideIndex+1]), "COUNT") {
				return nil, errors.New("ERR syntax error")
			}
			count, err = strconv.ParseInt(string(args[sideIndex+2]), 10, 64)
			if err != nil || count <= 0 {
				return nil, errors.New("ERR count should be greater than 0")
			}
		}
		key, items, found, err := s.store.ZSetMPop(keys, count, side == "MAX")
		if err != nil {
			return nil, err
		}
		if !found {
			return []byte("*-1\r\n"), nil
		}
		pairs := make([][]byte, 0, len(items))
		for _, item := range items {
			pairs = append(pairs, array(formatBulkString(item.Member), formatBulkString(formatZSetScore(item.Score))))
		}
		return array(formatBulkString([]byte(key)), array(pairs...)), nil

	case "ZMSCORE":
		scores, found, err := s.store.ZSetScores(string(args[1]), args[2:])
		if err != nil {
			return nil, err
		}
		parts := make([][]byte, len(scores))
		for i := range scores {
			if !found[i] {
				parts[i] = nullBulk()
			} else {
				parts[i] = formatBulkString(formatZSetScore(scores[i]))
			}
		}
		return array(parts...), nil

	case "ZRANDMEMBER":
		if len(args) == 2 {
			items, err := s.store.ZSetRandomMembers(string(args[1]), 1)
			if err != nil {
				return nil, err
			}
			if len(items) == 0 {
				return nullBulk(), nil
			}
			return formatBulkString(items[0].Member), nil
		}
		count, err := strconv.ParseInt(string(args[2]), 10, 64)
		if err != nil {
			return nil, errors.New("ERR value is not an integer or out of range")
		}
		withScores := false
		if len(args) == 4 {
			if !strings.EqualFold(string(args[3]), "WITHSCORES") {
				return nil, errors.New("ERR syntax error")
			}
			withScores = true
		}
		items, err := s.store.ZSetRandomMembers(string(args[1]), count)
		if err != nil {
			return nil, err
		}
		return zsetItemsResponse(items, withScores), nil

	case "ZSCAN":
		cursor, err := strconv.ParseUint(string(args[2]), 10, 64)
		if err != nil {
			return nil, errors.New("ERR invalid cursor")
		}
		options, err := parseScanOptions(args[3:], false, false)
		if err != nil {
			return nil, err
		}
		next, items, err := s.store.ZSetScanCompat(string(args[1]), cursor, options.Count, options.Pattern)
		if err != nil {
			return nil, err
		}
		return array(formatBulkString([]byte(strconv.FormatUint(next, 10))), zsetItemsResponse(items, true)), nil

	case "ZRANGESTORE":
		destination, source := string(args[1]), string(args[2])
		byScore, byLex, reverse, hasLimit := false, false, false, false
		var offset int64
		count := int64(-1)
		for i := 5; i < len(args); {
			switch strings.ToUpper(string(args[i])) {
			case "BYSCORE":
				if byScore || byLex {
					return nil, errors.New("ERR syntax error")
				}
				byScore = true
				i++
			case "BYLEX":
				if byScore || byLex {
					return nil, errors.New("ERR syntax error")
				}
				byLex = true
				i++
			case "REV":
				if reverse {
					return nil, errors.New("ERR syntax error")
				}
				reverse = true
				i++
			case "LIMIT":
				if hasLimit || i+2 >= len(args) {
					return nil, errors.New("ERR syntax error")
				}
				var err error
				offset, err = strconv.ParseInt(string(args[i+1]), 10, 64)
				if err != nil {
					return nil, errors.New("ERR value is not an integer or out of range")
				}
				count, err = strconv.ParseInt(string(args[i+2]), 10, 64)
				if err != nil {
					return nil, errors.New("ERR value is not an integer or out of range")
				}
				hasLimit = true
				i += 3
			default:
				return nil, errors.New("ERR syntax error")
			}
		}
		if byScore {
			var min, max engine.ZSetScoreBound
			var err error
			if reverse {
				max, err = parseZSetScoreBound(args[3])
				if err == nil {
					min, err = parseZSetScoreBound(args[4])
				}
			} else {
				min, err = parseZSetScoreBound(args[3])
				if err == nil {
					max, err = parseZSetScoreBound(args[4])
				}
			}
			if err != nil {
				return nil, err
			}
			stored, err := s.store.ZSetRangeStoreByScore(destination, source, min, max, reverse, offset, count)
			if err != nil {
				return nil, err
			}
			return integer(stored), nil
		}
		if byLex {
			var min, max engine.ZSetLexBound
			var err error
			if reverse {
				max, err = parseZSetLexBound(args[3])
				if err == nil {
					min, err = parseZSetLexBound(args[4])
				}
			} else {
				min, err = parseZSetLexBound(args[3])
				if err == nil {
					max, err = parseZSetLexBound(args[4])
				}
			}
			if err != nil {
				return nil, err
			}
			stored, err := s.store.ZSetRangeStoreByLex(destination, source, min, max, reverse, offset, count)
			if err != nil {
				return nil, err
			}
			return integer(stored), nil
		}
		if hasLimit {
			return nil, errors.New("ERR syntax error")
		}
		start, err := strconv.ParseInt(string(args[3]), 10, 64)
		if err != nil {
			return nil, errors.New("ERR value is not an integer or out of range")
		}
		stop, err := strconv.ParseInt(string(args[4]), 10, 64)
		if err != nil {
			return nil, errors.New("ERR value is not an integer or out of range")
		}
		stored, err := s.store.ZSetRangeStoreByRank(destination, source, start, stop, reverse)
		if err != nil {
			return nil, err
		}
		return integer(stored), nil
	}
	return nil, fmt.Errorf("ERR unknown command '%s'", cmd)
}
