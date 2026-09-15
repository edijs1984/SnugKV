package server

import (
	"errors"
	"snugkv/internal/engine"
	"strconv"
	"strings"
)

var zsetAlgebraCommands = map[string]commandInfo{
	"ZUNION":      {3, 0, 0, 0, 0, false},
	"ZINTER":      {3, 0, 0, 0, 0, false},
	"ZDIFF":       {3, 0, 0, 0, 0, false},
	"ZINTERCARD":  {3, 0, 0, 0, 0, false},
	"ZUNIONSTORE": {4, 0, 1, 1, 1, true},
	"ZINTERSTORE": {4, 0, 1, 1, 1, true},
	"ZDIFFSTORE":  {4, 0, 1, 1, 1, true},
}

func init() {
	for name, info := range zsetAlgebraCommands {
		commandTable[name] = info
	}
}

func isZSetAlgebraCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := zsetAlgebraCommands[strings.ToUpper(string(args[0]))]
	return ok
}

type zsetAlgebraRequest struct {
	destination string
	keys        []string
	weights     []float64
	aggregate   engine.ZSetAggregate
	withScores  bool
	limit       int64
}

func parseZSetAlgebraRequest(args [][]byte) (zsetAlgebraRequest, error) {
	var req zsetAlgebraRequest
	if len(args) < 3 {
		return req, errors.New("ERR syntax error")
	}
	cmd := strings.ToUpper(string(args[0]))
	store := strings.HasSuffix(cmd, "STORE")
	index := 1
	if store {
		if len(args) < 4 {
			return req, errors.New("ERR syntax error")
		}
		req.destination = string(args[index])
		index++
	}

	numKeys, err := strconv.ParseInt(string(args[index]), 10, 64)
	if err != nil || numKeys <= 0 {
		return req, errors.New("ERR at least 1 input key is needed for this command")
	}
	index++
	if numKeys > int64(len(args)-index) {
		return req, errors.New("ERR syntax error")
	}
	req.keys = make([]string, int(numKeys))
	for i := range req.keys {
		req.keys[i] = string(args[index+i])
	}
	index += int(numKeys)
	req.aggregate = engine.ZSetAggregateSum

	isDiff := cmd == "ZDIFF" || cmd == "ZDIFFSTORE"
	isCard := cmd == "ZINTERCARD"
	seenWeights := false
	seenAggregate := false
	seenWithScores := false
	seenLimit := false

	for index < len(args) {
		token := strings.ToUpper(string(args[index]))
		switch token {
		case "WEIGHTS":
			if isDiff || isCard || seenWeights || index+len(req.keys) >= len(args) {
				return req, errors.New("ERR syntax error")
			}
			req.weights = make([]float64, len(req.keys))
			for i := range req.weights {
				weight, parseErr := parseZSetScore(args[index+1+i])
				if parseErr != nil {
					return req, errors.New("ERR weight value is not a float")
				}
				req.weights[i] = weight
			}
			seenWeights = true
			index += 1 + len(req.keys)

		case "AGGREGATE":
			if isDiff || isCard || seenAggregate || index+1 >= len(args) {
				return req, errors.New("ERR syntax error")
			}
			switch strings.ToUpper(string(args[index+1])) {
			case "SUM":
				req.aggregate = engine.ZSetAggregateSum
			case "MIN":
				req.aggregate = engine.ZSetAggregateMin
			case "MAX":
				req.aggregate = engine.ZSetAggregateMax
			case "COUNT":
				req.aggregate = engine.ZSetAggregateCount
			default:
				return req, errors.New("ERR syntax error")
			}
			seenAggregate = true
			index += 2

		case "WITHSCORES":
			if store || isCard || seenWithScores {
				return req, errors.New("ERR syntax error")
			}
			req.withScores = true
			seenWithScores = true
			index++

		case "LIMIT":
			if !isCard || seenLimit || index+1 >= len(args) {
				return req, errors.New("ERR syntax error")
			}
			limit, parseErr := strconv.ParseInt(string(args[index+1]), 10, 64)
			if parseErr != nil || limit < 0 {
				return req, errors.New("ERR LIMIT can't be negative")
			}
			req.limit = limit
			seenLimit = true
			index += 2

		default:
			return req, errors.New("ERR syntax error")
		}
	}
	return req, nil
}

func (s *Server) executeZSetAlgebra(args [][]byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("ERR empty command")
	}
	cmd := strings.ToUpper(string(args[0]))
	info, ok := zsetAlgebraCommands[cmd]
	if !ok {
		return nil, errors.New("ERR unknown sorted set algebra command")
	}
	if len(args) < info.min || info.max > 0 && len(args) > info.max {
		return nil, errors.New("ERR wrong number of arguments")
	}
	req, err := parseZSetAlgebraRequest(args)
	if err != nil {
		return nil, err
	}

	switch cmd {
	case "ZUNION":
		items, err := s.store.ZSetUnion(req.keys, req.weights, req.aggregate)
		if err != nil { return nil, err }
		return zsetItemsResponse(items, req.withScores), nil
	case "ZINTER":
		items, err := s.store.ZSetIntersect(req.keys, req.weights, req.aggregate)
		if err != nil { return nil, err }
		return zsetItemsResponse(items, req.withScores), nil
	case "ZDIFF":
		items, err := s.store.ZSetDiff(req.keys)
		if err != nil { return nil, err }
		return zsetItemsResponse(items, req.withScores), nil
	case "ZINTERCARD":
		count, err := s.store.ZSetIntersectCardinality(req.keys, req.limit)
		if err != nil { return nil, err }
		return integer(count), nil
	case "ZUNIONSTORE":
		count, err := s.store.ZSetUnionStore(req.destination, req.keys, req.weights, req.aggregate)
		if err != nil { return nil, err }
		return integer(count), nil
	case "ZINTERSTORE":
		count, err := s.store.ZSetIntersectStore(req.destination, req.keys, req.weights, req.aggregate)
		if err != nil { return nil, err }
		return integer(count), nil
	case "ZDIFFSTORE":
		count, err := s.store.ZSetDiffStore(req.destination, req.keys)
		if err != nil { return nil, err }
		return integer(count), nil
	}
	return nil, errors.New("ERR unknown sorted set algebra command")
}

// zsetAlgebraInputKeys returns all real input keys plus the STORE destination.
// It is used only by memory-pressure eviction protection; durability journals
// only the destination because sources are read-only.
func zsetAlgebraInputKeys(args [][]byte) []string {
	if len(args) < 3 || !isZSetAlgebraCommand(args) {
		return nil
	}
	cmd := strings.ToUpper(string(args[0]))
	store := strings.HasSuffix(cmd, "STORE")
	index := 1
	keys := make([]string, 0)
	if store {
		if len(args) < 4 { return nil }
		keys = append(keys, string(args[index]))
		index++
	}
	numKeys, err := strconv.ParseInt(string(args[index]), 10, 64)
	if err != nil || numKeys <= 0 {
		return keys
	}
	index++
	for i := int64(0); i < numKeys && index < len(args); i++ {
		keys = append(keys, string(args[index]))
		index++
	}
	return keys
}
