package server

import (
	"errors"
	"fmt"
	"math"
	"snugkv/internal/engine"
	"strconv"
	"strings"
)

var zsetCommands = map[string]commandInfo{
	"ZADD":      {4, 0, 1, 1, 1, true},
	"ZREM":      {3, 0, 1, 1, 1, true},
	"ZSCORE":    {3, 3, 1, 1, 1, false},
	"ZCARD":     {2, 2, 1, 1, 1, false},
	"ZRANK":     {3, 4, 1, 1, 1, false},
	"ZREVRANK":  {3, 4, 1, 1, 1, false},
	"ZRANGE":    {4, 5, 1, 1, 1, false},
	"ZREVRANGE": {4, 5, 1, 1, 1, false},
}

func init() {
	for name, info := range zsetCommands {
		commandTable[name] = info
	}
}

func isZSetCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := zsetCommands[strings.ToUpper(string(args[0]))]
	return ok
}

func parseZSetScore(arg []byte) (float64, error) {
	score, err := strconv.ParseFloat(string(arg), 64)
	if err != nil || math.IsNaN(score) {
		return 0, errors.New("ERR value is not a valid float")
	}
	if score == 0 {
		score = 0
	}
	return score, nil
}

func formatZSetScore(score float64) []byte {
	if math.IsInf(score, 1) {
		return []byte("inf")
	}
	if math.IsInf(score, -1) {
		return []byte("-inf")
	}
	return []byte(strconv.FormatFloat(score, 'g', -1, 64))
}

func (s *Server) executeZSet(args [][]byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("ERR empty command")
	}
	cmd := strings.ToUpper(string(args[0]))
	info, ok := zsetCommands[cmd]
	if !ok {
		return nil, fmt.Errorf("ERR unknown command '%s'", cmd)
	}
	if len(args) < info.min || info.max > 0 && len(args) > info.max {
		return nil, fmt.Errorf("ERR wrong number of arguments for '%s' command", strings.ToLower(cmd))
	}

	key := string(args[1])
	switch cmd {
	case "ZADD":
		options := engine.ZSetAddOptions{}
		i := 2
		for i < len(args) {
			switch strings.ToUpper(string(args[i])) {
			case "NX":
				options.NX = true
			case "XX":
				options.XX = true
			case "GT":
				options.GT = true
			case "LT":
				options.LT = true
			case "CH":
				options.CH = true
			case "INCR":
				options.INCR = true
			default:
				goto pairs
			}
			i++
		}
	pairs:
		if i >= len(args) || (len(args)-i)%2 != 0 {
			return nil, errors.New("ERR syntax error")
		}
		pairs := make([]engine.ZSetItem, 0, (len(args)-i)/2)
		for ; i < len(args); i += 2 {
			score, err := parseZSetScore(args[i])
			if err != nil {
				return nil, err
			}
			pairs = append(pairs, engine.ZSetItem{Score: score, Member: args[i+1]})
		}
		count, incremented, score, err := s.store.ZSetAdd(key, pairs, options)
		if err != nil {
			return nil, err
		}
		if options.INCR {
			if !incremented {
				return nullBulk(), nil
			}
			return formatBulkString(formatZSetScore(score)), nil
		}
		return integer(count), nil

	case "ZREM":
		removed, err := s.store.ZSetRemove(key, args[2:])
		if err != nil {
			return nil, err
		}
		return integer(removed), nil

	case "ZSCORE":
		score, found, err := s.store.ZSetScore(key, args[2])
		if err != nil {
			return nil, err
		}
		if !found {
			return nullBulk(), nil
		}
		return formatBulkString(formatZSetScore(score)), nil

	case "ZCARD":
		count, err := s.store.ZSetCard(key)
		if err != nil {
			return nil, err
		}
		return integer(count), nil

	case "ZRANK", "ZREVRANK":
		withScore := false
		if len(args) == 4 {
			if !strings.EqualFold(string(args[3]), "WITHSCORE") {
				return nil, errors.New("ERR syntax error")
			}
			withScore = true
		}
		rank, found, err := s.store.ZSetRank(key, args[2], cmd == "ZREVRANK")
		if err != nil {
			return nil, err
		}
		if !found {
			return nullBulk(), nil
		}
		if !withScore {
			return integer(rank), nil
		}
		score, _, err := s.store.ZSetScore(key, args[2])
		if err != nil {
			return nil, err
		}
		return array(integer(rank), formatBulkString(formatZSetScore(score))), nil

	case "ZRANGE", "ZREVRANGE":
		start, err := strconv.ParseInt(string(args[2]), 10, 64)
		if err != nil {
			return nil, errors.New("ERR value is not an integer or out of range")
		}
		stop, err := strconv.ParseInt(string(args[3]), 10, 64)
		if err != nil {
			return nil, errors.New("ERR value is not an integer or out of range")
		}
		withScores := false
		if len(args) == 5 {
			if !strings.EqualFold(string(args[4]), "WITHSCORES") {
				return nil, errors.New("ERR syntax error")
			}
			withScores = true
		}
		items, err := s.store.ZSetRange(key, start, stop, cmd == "ZREVRANGE")
		if err != nil {
			return nil, err
		}
		response := make([][]byte, 0, len(items)*2)
		for _, item := range items {
			response = append(response, formatBulkString(item.Member))
			if withScores {
				response = append(response, formatBulkString(formatZSetScore(item.Score)))
			}
		}
		return array(response...), nil
	}

	return nil, fmt.Errorf("ERR unknown command '%s'", cmd)
}
