package server

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"snugkv/internal/engine"
)

var hashCommands = map[string]commandInfo{
	"HSET":         {4, 0, 1, 1, 1, true},
	"HGET":         {3, 3, 1, 1, 1, false},
	"HDEL":         {3, 0, 1, 1, 1, true},
	"HLEN":         {2, 2, 1, 1, 1, false},
	"HEXISTS":      {3, 3, 1, 1, 1, false},
	"HMGET":        {3, 0, 1, 1, 1, false},
	"HGETALL":      {2, 2, 1, 1, 1, false},
	"HKEYS":        {2, 2, 1, 1, 1, false},
	"HVALS":        {2, 2, 1, 1, 1, false},
	"HSETNX":       {4, 4, 1, 1, 1, true},
	"HSTRLEN":      {3, 3, 1, 1, 1, false},
	"HINCRBY":      {4, 4, 1, 1, 1, true},
	"HINCRBYFLOAT": {4, 4, 1, 1, 1, true},
	"HSCAN":        {3, 0, 1, 1, 1, false},
}

func init() {
	for name, info := range hashCommands {
		commandTable[name] = info
	}
}

func isHashCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := hashCommands[strings.ToUpper(string(args[0]))]
	return ok
}

// executeCommand is the common command dispatcher used by the pressure/eviction
// layer. HASH commands live in a separate file so the main server switch does
// not keep growing indefinitely.
func (s *Server) executeCommand(args [][]byte) ([]byte, error) {
	if isHashCommand(args) {
		return s.executeHash(args)
	}
	return s.execute(args)
}

func (s *Server) executeHash(args [][]byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("ERR empty command")
	}

	cmd := strings.ToUpper(string(args[0]))
	info, ok := hashCommands[cmd]
	if !ok {
		return nil, fmt.Errorf("ERR unknown command '%s'", cmd)
	}
	if len(args) < info.min || info.max > 0 && len(args) > info.max {
		return nil, fmt.Errorf("ERR wrong number of arguments for '%s' command", strings.ToLower(cmd))
	}

	key := string(args[1])

	switch cmd {
	case "HSET":
		if len(args)%2 != 0 {
			return nil, errors.New("ERR wrong number of arguments for 'hset' command")
		}
		fields := make([][]byte, 0, (len(args)-2)/2)
		values := make([][]byte, 0, (len(args)-2)/2)
		for i := 2; i < len(args); i += 2 {
			fields = append(fields, args[i])
			values = append(values, args[i+1])
		}
		added, err := s.store.HashSet(key, fields, values)
		if err != nil {
			return nil, err
		}
		return integer(added), nil

	case "HGET":
		value, found, err := s.store.HashGet(key, args[2])
		if err != nil {
			return nil, err
		}
		return optionalBulk(value, found), nil

	case "HDEL":
		deleted, err := s.store.HashDel(key, args[2:])
		if err != nil {
			return nil, err
		}
		return integer(deleted), nil

	case "HLEN":
		length, err := s.store.HashLen(key)
		if err != nil {
			return nil, err
		}
		return integer(length), nil

	case "HEXISTS":
		_, found, err := s.store.HashGet(key, args[2])
		if err != nil {
			return nil, err
		}
		if found {
			return integer(1), nil
		}
		return integer(0), nil

	case "HMGET":
		pairs, err := s.store.HashGetAll(key)
		if err != nil {
			return nil, err
		}
		items := make([][]byte, 0, len(args)-2)
		for _, field := range args[2:] {
			value, found := hashPairLookup(pairs, field)
			items = append(items, optionalBulk(value, found))
		}
		return array(items...), nil

	case "HGETALL":
		pairs, err := s.store.HashGetAll(key)
		if err != nil {
			return nil, err
		}
		items := make([][]byte, 0, 2*len(pairs))
		for _, pair := range pairs {
			items = append(items, formatBulkString(pair.Field), formatBulkString(pair.Value))
		}
		return array(items...), nil

	case "HKEYS":
		pairs, err := s.store.HashGetAll(key)
		if err != nil {
			return nil, err
		}
		items := make([][]byte, 0, len(pairs))
		for _, pair := range pairs {
			items = append(items, formatBulkString(pair.Field))
		}
		return array(items...), nil

	case "HVALS":
		pairs, err := s.store.HashGetAll(key)
		if err != nil {
			return nil, err
		}
		items := make([][]byte, 0, len(pairs))
		for _, pair := range pairs {
			items = append(items, formatBulkString(pair.Value))
		}
		return array(items...), nil

	case "HSETNX":
		inserted, err := s.store.HashSetNX(key, args[2], args[3])
		if err != nil {
			return nil, err
		}
		if inserted {
			return integer(1), nil
		}
		return integer(0), nil

	case "HSTRLEN":
		value, found, err := s.store.HashGet(key, args[2])
		if err != nil {
			return nil, err
		}
		if !found {
			return integer(0), nil
		}
		return integer(int64(len(value))), nil

	case "HINCRBY":
		increment, err := strconv.ParseInt(string(args[3]), 10, 64)
		if err != nil {
			return nil, errors.New("ERR value is not an integer or out of range")
		}
		result, err := s.store.HashIncrBy(key, args[2], increment)
		if err != nil {
			return nil, err
		}
		return integer(result), nil

	case "HINCRBYFLOAT":
		increment, err := strconv.ParseFloat(string(args[3]), 64)
		if err != nil || math.IsNaN(increment) || math.IsInf(increment, 0) {
			return nil, errors.New("ERR value is not a valid float")
		}
		result, err := s.store.HashIncrByFloat(key, args[2], increment)
		if err != nil {
			return nil, err
		}
		return formatBulkString([]byte(result)), nil

	case "HSCAN":
		cursor, err := strconv.ParseUint(string(args[2]), 10, 64)
		if err != nil {
			return nil, errors.New("ERR invalid cursor")
		}

		count := 10
		var pattern []byte
		hasPattern := false
		noValues := false

		for i := 3; i < len(args); {
			switch strings.ToUpper(string(args[i])) {
			case "MATCH":
				if i+1 >= len(args) {
					return nil, errors.New("ERR syntax error")
				}
				pattern = args[i+1]
				hasPattern = true
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

			case "NOVALUES":
				noValues = true
				i++

			default:
				return nil, errors.New("ERR syntax error")
			}
		}

		next, pairs, err := s.store.HashScan(key, cursor, count, pattern, hasPattern)
		if err != nil {
			return nil, err
		}
		items := make([][]byte, 0, 2*len(pairs))
		for _, pair := range pairs {
			items = append(items, formatBulkString(pair.Field))
			if !noValues {
				items = append(items, formatBulkString(pair.Value))
			}
		}
		return array(
			formatBulkString([]byte(strconv.FormatUint(next, 10))),
			array(items...),
		), nil
	}

	return nil, fmt.Errorf("ERR unknown command '%s'", cmd)
}

func hashPairLookup(pairs []engine.HashPair, field []byte) ([]byte, bool) {
	index := sort.Search(len(pairs), func(i int) bool {
		return bytes.Compare(pairs[i].Field, field) >= 0
	})
	if index >= len(pairs) || !bytes.Equal(pairs[index].Field, field) {
		return nil, false
	}
	return pairs[index].Value, true
}
