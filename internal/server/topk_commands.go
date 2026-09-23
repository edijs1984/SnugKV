package server

import (
	"errors"
	"strconv"
	"strings"

	"snugkv/internal/engine"
)

var topKCommands = map[string]commandInfo{
	"TOPK.RESERVE": {3, 6, 1, 1, 1, true},
	"TOPK.ADD":     {3, 0, 1, 1, 1, true},
	"TOPK.INCRBY":  {4, 0, 1, 1, 1, true},
	"TOPK.QUERY":   {3, 0, 1, 1, 1, false},
	"TOPK.COUNT":   {3, 0, 1, 1, 1, false},
	"TOPK.LIST":    {2, 3, 1, 1, 1, false},
	"TOPK.INFO":    {2, 2, 1, 1, 1, false},
}

const topKIncrementError = "TopK: increment must be an integer greater or equal to 0                            and smaller or equal to 100,000"

func init() {
	for name, info := range topKCommands {
		commandTable[name] = info
	}
}

func isTopKCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := topKCommands[strings.ToUpper(string(args[0]))]
	return ok
}

func topKAddReply(results []engine.TopKAddResult) []byte {
	items := make([][]byte, len(results))
	for i, result := range results {
		if result.HasExpelled {
			items[i] = formatBulkString(result.Expelled)
		} else {
			items[i] = nullBulk()
		}
	}
	return array(items...)
}

func topKBoolReply(values []bool) []byte {
	items := make([][]byte, len(values))
	for i, value := range values {
		if value {
			items[i] = integer(1)
		} else {
			items[i] = integer(0)
		}
	}
	return array(items...)
}

func topKCountReply(values []uint32) []byte {
	items := make([][]byte, len(values))
	for i, value := range values {
		items[i] = integer(int64(value))
	}
	return array(items...)
}

func (s *Server) executeTopK(args [][]byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("ERR empty command")
	}
	cmd := strings.ToUpper(string(args[0]))
	info, ok := topKCommands[cmd]
	if !ok {
		return nil, errors.New("ERR unknown TopK command")
	}
	if len(args) < info.min || info.max > 0 && len(args) > info.max {
		return nil, errors.New("ERR wrong number of arguments for '" + strings.ToLower(cmd) + "' command")
	}

	key := string(args[1])
	switch cmd {
	case "TOPK.RESERVE":
		if len(args) != 3 && len(args) != 6 {
			return nil, errors.New("ERR wrong number of arguments for 'topk.reserve' command")
		}
		k, err := strconv.ParseUint(string(args[2]), 10, 32)
		if err != nil || k < 1 {
			return nil, errors.New("TopK: invalid k")
		}
		custom := len(args) == 6
		var width, depth uint64
		decay := topKDefaultDecayServer
		if custom {
			width, err = strconv.ParseUint(string(args[3]), 10, 32)
			if err != nil || width < 1 {
				return nil, errors.New("TopK: invalid width")
			}
			depth, err = strconv.ParseUint(string(args[4]), 10, 32)
			if err != nil || depth < 1 {
				return nil, errors.New("TopK: invalid depth")
			}
			decay, err = strconv.ParseFloat(string(args[5]), 64)
			if err != nil || !(decay > 0 && decay <= 1) {
				return nil, errors.New("TopK: invalid decay value. must be '<= 1' & '> 0'")
			}
		}
		if err := s.store.TopKReserve(key, uint32(k), custom, uint32(width), uint32(depth), decay); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil

	case "TOPK.ADD":
		increments := make([]uint32, len(args)-2)
		for i := range increments {
			increments[i] = 1
		}
		results, err := s.store.TopKAdd(key, args[2:], increments)
		if err != nil {
			return nil, err
		}
		return topKAddReply(results), nil

	case "TOPK.INCRBY":
		if len(args) < 4 || len(args)%2 != 0 {
			return nil, errors.New("ERR wrong number of arguments for 'topk.incrby' command")
		}
		// RedisBloom checks the key before parsing individual increments.
		if _, err := s.store.TopKInfo(key); err != nil {
			return nil, err
		}
		replies := make([][]byte, 0, (len(args)-2)/2)
		for pos := 2; pos < len(args); pos += 2 {
			increment, err := strconv.ParseInt(string(args[pos+1]), 10, 64)
			if err != nil || increment < 0 || increment > 100000 {
				replies = append(replies, []byte("-"+topKIncrementError+"\r\n"))
				break
			}
			results, err := s.store.TopKAdd(key, [][]byte{args[pos]}, []uint32{uint32(increment)})
			if err != nil {
				return nil, err
			}
			if results[0].HasExpelled {
				replies = append(replies, formatBulkString(results[0].Expelled))
			} else {
				replies = append(replies, nullBulk())
			}
		}
		return array(replies...), nil

	case "TOPK.QUERY":
		results, err := s.store.TopKQuery(key, args[2:])
		if err != nil {
			return nil, err
		}
		return topKBoolReply(results), nil

	case "TOPK.COUNT":
		results, err := s.store.TopKCount(key, args[2:])
		if err != nil {
			return nil, err
		}
		return topKCountReply(results), nil

	case "TOPK.LIST":
		withCount := false
		if len(args) == 3 {
			if !strings.EqualFold(string(args[2]), "WITHCOUNT") {
				return nil, errors.New("WITHCOUNT keyword expected")
			}
			withCount = true
		}
		entries, err := s.store.TopKList(key)
		if err != nil {
			return nil, err
		}
		items := make([][]byte, 0, len(entries)*(1+btoi(withCount)))
		for _, entry := range entries {
			items = append(items, formatBulkString(entry.Item))
			if withCount {
				items = append(items, integer(int64(entry.Count)))
			}
		}
		return array(items...), nil

	case "TOPK.INFO":
		info, err := s.store.TopKInfo(key)
		if err != nil {
			return nil, err
		}
		return array(
			[]byte("+k\r\n"), integer(int64(info.K)),
			[]byte("+width\r\n"), integer(int64(info.Width)),
			[]byte("+depth\r\n"), integer(int64(info.Depth)),
			[]byte("+decay\r\n"), formatBulkString([]byte(strconv.FormatFloat(info.Decay, 'g', -1, 64))),
		), nil
	}
	return nil, errors.New("ERR unknown TopK command")
}

const topKDefaultDecayServer = 0.9

func btoi(value bool) int {
	if value {
		return 1
	}
	return 0
}
