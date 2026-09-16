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
	"ZADD":              {4, 0, 1, 1, 1, true},
	"ZREM":              {3, 0, 1, 1, 1, true},
	"ZINCRBY":           {4, 4, 1, 1, 1, true},
	"ZSCORE":            {3, 3, 1, 1, 1, false},
	"ZCARD":             {2, 2, 1, 1, 1, false},
	"ZCOUNT":            {4, 4, 1, 1, 1, false},
	"ZLEXCOUNT":         {4, 4, 1, 1, 1, false},
	"ZRANK":             {3, 4, 1, 1, 1, false},
	"ZREVRANK":          {3, 4, 1, 1, 1, false},
	"ZRANGE":            {4, 0, 1, 1, 1, false},
	"ZREVRANGE":         {4, 5, 1, 1, 1, false},
	"ZRANGEBYSCORE":     {4, 0, 1, 1, 1, false},
	"ZREVRANGEBYSCORE":  {4, 0, 1, 1, 1, false},
	"ZRANGEBYLEX":       {4, 0, 1, 1, 1, false},
	"ZREVRANGEBYLEX":    {4, 0, 1, 1, 1, false},
	"ZREMRANGEBYRANK":   {4, 4, 1, 1, 1, true},
	"ZREMRANGEBYSCORE":  {4, 4, 1, 1, 1, true},
	"ZREMRANGEBYLEX":    {4, 4, 1, 1, 1, true},
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
	text := strings.ToLower(string(arg))
	var score float64
	var err error
	switch text {
	case "+inf", "inf":
		score = math.Inf(1)
	case "-inf":
		score = math.Inf(-1)
	default:
		score, err = strconv.ParseFloat(string(arg), 64)
		if err != nil {
			return 0, errors.New("ERR value is not a valid float")
		}
	}
	if math.IsNaN(score) {
		return 0, errors.New("ERR value is not a valid float")
	}
	if score == 0 {
		score = 0
	}
	return score, nil
}

func parseZSetScoreBound(arg []byte) (engine.ZSetScoreBound, error) {
	if len(arg) == 0 {
		return engine.ZSetScoreBound{}, errors.New("ERR min or max is not a float")
	}
	exclusive := arg[0] == '('
	if exclusive {
		arg = arg[1:]
		if len(arg) == 0 {
			return engine.ZSetScoreBound{}, errors.New("ERR min or max is not a float")
		}
	}
	score, err := parseZSetScore(arg)
	if err != nil {
		return engine.ZSetScoreBound{}, errors.New("ERR min or max is not a float")
	}
	return engine.ZSetScoreBound{Score: score, Exclusive: exclusive}, nil
}

func parseZSetLexBound(arg []byte) (engine.ZSetLexBound, error) {
	if len(arg) == 1 && arg[0] == '-' {
		return engine.ZSetLexBound{Infinite: -1}, nil
	}
	if len(arg) == 1 && arg[0] == '+' {
		return engine.ZSetLexBound{Infinite: 1}, nil
	}
	if len(arg) == 0 || arg[0] != '[' && arg[0] != '(' {
		return engine.ZSetLexBound{}, errors.New("ERR min or max not valid string range item")
	}
	return engine.ZSetLexBound{Value: append([]byte(nil), arg[1:]...), Exclusive: arg[0] == '('}, nil
}

func formatZSetScore(score float64) []byte {
	if math.IsInf(score, 1) {
		return []byte("inf")
	}
	if math.IsInf(score, -1) {
		return []byte("-inf")
	}
	if score == 0 {
		if math.Signbit(score) {
			return []byte("-0")
		}
		return []byte("0")
	}

	// Redis d2string() first emits losslessly integral doubles as signed
	// decimal integers before falling back to dtoa formatting. GEO scores are
	// 52-bit integers stored in ZSET doubles, so this avoids scientific notation
	// for values such as 3479099956230698 and matches Redis wire output.
	const redisDoubleIntLimit = float64(1 << 62)
	if score >= -redisDoubleIntLimit && score <= redisDoubleIntLimit {
		integerScore := int64(score)
		if float64(integerScore) == score {
			return []byte(strconv.FormatInt(integerScore, 10))
		}
	}

	return []byte(strconv.FormatFloat(score, 'g', -1, 64))
}

func zsetItemsResponse(items []engine.ZSetItem, withScores bool) []byte {
	response := make([][]byte, 0, len(items)*2)
	for _, item := range items {
		response = append(response, formatBulkString(item.Member))
		if withScores {
			response = append(response, formatBulkString(formatZSetScore(item.Score)))
		}
	}
	return array(response...)
}

func parseZSetRangeOptions(args [][]byte, start int, allowWithScores bool) (withScores bool, offset, count int64, err error) {
	count = -1
	seenLimit := false
	for i := start; i < len(args); {
		switch strings.ToUpper(string(args[i])) {
		case "WITHSCORES":
			if !allowWithScores || withScores {
				return false, 0, 0, errors.New("ERR syntax error")
			}
			withScores = true
			i++
		case "LIMIT":
			if seenLimit || i+2 >= len(args) {
				return false, 0, 0, errors.New("ERR syntax error")
			}
			offset, err = strconv.ParseInt(string(args[i+1]), 10, 64)
			if err != nil {
				return false, 0, 0, errors.New("ERR value is not an integer or out of range")
			}
			count, err = strconv.ParseInt(string(args[i+2]), 10, 64)
			if err != nil {
				return false, 0, 0, errors.New("ERR value is not an integer or out of range")
			}
			seenLimit = true
			i += 3
		default:
			return false, 0, 0, errors.New("ERR syntax error")
		}
	}
	return withScores, offset, count, nil
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
			case "NX": options.NX = true
			case "XX": options.XX = true
			case "GT": options.GT = true
			case "LT": options.LT = true
			case "CH": options.CH = true
			case "INCR": options.INCR = true
			default: goto pairs
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
			if err != nil { return nil, err }
			pairs = append(pairs, engine.ZSetItem{Score: score, Member: args[i+1]})
		}
		count, incremented, score, err := s.store.ZSetAdd(key, pairs, options)
		if err != nil { return nil, err }
		if options.INCR {
			if !incremented { return nullBulk(), nil }
			return formatBulkString(formatZSetScore(score)), nil
		}
		return integer(count), nil

	case "ZINCRBY":
		increment, err := parseZSetScore(args[2])
		if err != nil { return nil, err }
		_, applied, score, err := s.store.ZSetAdd(key, []engine.ZSetItem{{Score: increment, Member: args[3]}}, engine.ZSetAddOptions{INCR: true})
		if err != nil { return nil, err }
		if !applied { return nullBulk(), nil }
		return formatBulkString(formatZSetScore(score)), nil

	case "ZREM":
		removed, err := s.store.ZSetRemove(key, args[2:])
		if err != nil { return nil, err }
		return integer(removed), nil

	case "ZSCORE":
		score, found, err := s.store.ZSetScore(key, args[2])
		if err != nil { return nil, err }
		if !found { return nullBulk(), nil }
		return formatBulkString(formatZSetScore(score)), nil

	case "ZCARD":
		count, err := s.store.ZSetCard(key)
		if err != nil { return nil, err }
		return integer(count), nil

	case "ZCOUNT":
		min, err := parseZSetScoreBound(args[2])
		if err != nil { return nil, err }
		max, err := parseZSetScoreBound(args[3])
		if err != nil { return nil, err }
		count, err := s.store.ZSetCount(key, min, max)
		if err != nil { return nil, err }
		return integer(count), nil

	case "ZLEXCOUNT":
		min, err := parseZSetLexBound(args[2])
		if err != nil { return nil, err }
		max, err := parseZSetLexBound(args[3])
		if err != nil { return nil, err }
		count, err := s.store.ZSetLexCount(key, min, max)
		if err != nil { return nil, err }
		return integer(count), nil

	case "ZRANK", "ZREVRANK":
		withScore := false
		if len(args) == 4 {
			if !strings.EqualFold(string(args[3]), "WITHSCORE") { return nil, errors.New("ERR syntax error") }
			withScore = true
		}
		rank, found, err := s.store.ZSetRank(key, args[2], cmd == "ZREVRANK")
		if err != nil { return nil, err }
		if !found { return nullBulk(), nil }
		if !withScore { return integer(rank), nil }
		score, _, err := s.store.ZSetScore(key, args[2])
		if err != nil { return nil, err }
		return array(integer(rank), formatBulkString(formatZSetScore(score))), nil

	case "ZRANGEBYSCORE", "ZREVRANGEBYSCORE":
		reverse := cmd == "ZREVRANGEBYSCORE"
		var min, max engine.ZSetScoreBound
		var err error
		if reverse {
			max, err = parseZSetScoreBound(args[2]); if err == nil { min, err = parseZSetScoreBound(args[3]) }
		} else {
			min, err = parseZSetScoreBound(args[2]); if err == nil { max, err = parseZSetScoreBound(args[3]) }
		}
		if err != nil { return nil, err }
		withScores, offset, count, err := parseZSetRangeOptions(args, 4, true)
		if err != nil { return nil, err }
		items, err := s.store.ZSetRangeByScore(key, min, max, reverse, offset, count)
		if err != nil { return nil, err }
		return zsetItemsResponse(items, withScores), nil

	case "ZRANGEBYLEX", "ZREVRANGEBYLEX":
		reverse := cmd == "ZREVRANGEBYLEX"
		var min, max engine.ZSetLexBound
		var err error
		if reverse {
			max, err = parseZSetLexBound(args[2]); if err == nil { min, err = parseZSetLexBound(args[3]) }
		} else {
			min, err = parseZSetLexBound(args[2]); if err == nil { max, err = parseZSetLexBound(args[3]) }
		}
		if err != nil { return nil, err }
		_, offset, count, err := parseZSetRangeOptions(args, 4, false)
		if err != nil { return nil, err }
		items, err := s.store.ZSetRangeByLex(key, min, max, reverse, offset, count)
		if err != nil { return nil, err }
		return zsetItemsResponse(items, false), nil

	case "ZRANGE":
		byScore, byLex, reverse, withScores, hasLimit := false, false, false, false, false
		var offset int64
		count := int64(-1)
		for i := 4; i < len(args); {
			switch strings.ToUpper(string(args[i])) {
			case "BYSCORE":
				if byScore || byLex { return nil, errors.New("ERR syntax error") }
				byScore = true; i++
			case "BYLEX":
				if byScore || byLex { return nil, errors.New("ERR syntax error") }
				byLex = true; i++
			case "REV":
				if reverse { return nil, errors.New("ERR syntax error") }
				reverse = true; i++
			case "WITHSCORES":
				if withScores { return nil, errors.New("ERR syntax error") }
				withScores = true; i++
			case "LIMIT":
				if hasLimit || i+2 >= len(args) { return nil, errors.New("ERR syntax error") }
				var err error
				offset, err = strconv.ParseInt(string(args[i+1]), 10, 64)
				if err != nil { return nil, errors.New("ERR value is not an integer or out of range") }
				count, err = strconv.ParseInt(string(args[i+2]), 10, 64)
				if err != nil { return nil, errors.New("ERR value is not an integer or out of range") }
				hasLimit = true; i += 3
			default:
				return nil, errors.New("ERR syntax error")
			}
		}
		if byScore {
			var min, max engine.ZSetScoreBound
			var err error
			if reverse { max, err = parseZSetScoreBound(args[2]); if err == nil { min, err = parseZSetScoreBound(args[3]) } } else { min, err = parseZSetScoreBound(args[2]); if err == nil { max, err = parseZSetScoreBound(args[3]) } }
			if err != nil { return nil, err }
			items, err := s.store.ZSetRangeByScore(key, min, max, reverse, offset, count)
			if err != nil { return nil, err }
			return zsetItemsResponse(items, withScores), nil
		}
		if byLex {
			var min, max engine.ZSetLexBound
			var err error
			if reverse { max, err = parseZSetLexBound(args[2]); if err == nil { min, err = parseZSetLexBound(args[3]) } } else { min, err = parseZSetLexBound(args[2]); if err == nil { max, err = parseZSetLexBound(args[3]) } }
			if err != nil { return nil, err }
			items, err := s.store.ZSetRangeByLex(key, min, max, reverse, offset, count)
			if err != nil { return nil, err }
			return zsetItemsResponse(items, withScores), nil
		}
		if hasLimit { return nil, errors.New("ERR syntax error") }
		start, err := strconv.ParseInt(string(args[2]), 10, 64)
		if err != nil { return nil, errors.New("ERR value is not an integer or out of range") }
		stop, err := strconv.ParseInt(string(args[3]), 10, 64)
		if err != nil { return nil, errors.New("ERR value is not an integer or out of range") }
		items, err := s.store.ZSetRange(key, start, stop, reverse)
		if err != nil { return nil, err }
		return zsetItemsResponse(items, withScores), nil

	case "ZREVRANGE":
		start, err := strconv.ParseInt(string(args[2]), 10, 64)
		if err != nil { return nil, errors.New("ERR value is not an integer or out of range") }
		stop, err := strconv.ParseInt(string(args[3]), 10, 64)
		if err != nil { return nil, errors.New("ERR value is not an integer or out of range") }
		withScores := false
		if len(args) == 5 {
			if !strings.EqualFold(string(args[4]), "WITHSCORES") { return nil, errors.New("ERR syntax error") }
			withScores = true
		}
		items, err := s.store.ZSetRange(key, start, stop, true)
		if err != nil { return nil, err }
		return zsetItemsResponse(items, withScores), nil

	case "ZREMRANGEBYRANK":
		start, err := strconv.ParseInt(string(args[2]), 10, 64)
		if err != nil { return nil, errors.New("ERR value is not an integer or out of range") }
		stop, err := strconv.ParseInt(string(args[3]), 10, 64)
		if err != nil { return nil, errors.New("ERR value is not an integer or out of range") }
		removed, err := s.store.ZSetRemoveRangeByRank(key, start, stop)
		if err != nil { return nil, err }
		return integer(removed), nil

	case "ZREMRANGEBYSCORE":
		min, err := parseZSetScoreBound(args[2])
		if err != nil { return nil, err }
		max, err := parseZSetScoreBound(args[3])
		if err != nil { return nil, err }
		removed, err := s.store.ZSetRemoveRangeByScore(key, min, max)
		if err != nil { return nil, err }
		return integer(removed), nil

	case "ZREMRANGEBYLEX":
		min, err := parseZSetLexBound(args[2])
		if err != nil { return nil, err }
		max, err := parseZSetLexBound(args[3])
		if err != nil { return nil, err }
		removed, err := s.store.ZSetRemoveRangeByLex(key, min, max)
		if err != nil { return nil, err }
		return integer(removed), nil
	}

	return nil, fmt.Errorf("ERR unknown command '%s'", cmd)
}
