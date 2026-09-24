package server

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"snugkv/internal/engine"
)

var hashCommands = map[string]commandInfo{
	"HSET":         {4, 0, 1, 1, 1, true},
	"HMSET":        {4, 0, 1, 1, 1, true},
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
	"HRANDFIELD":   {2, 4, 1, 1, 1, false},
	"HEXPIRE":       {6, 0, 1, 1, 1, true},
	"HPEXPIRE":      {6, 0, 1, 1, 1, true},
	"HEXPIREAT":     {6, 0, 1, 1, 1, true},
	"HPEXPIREAT":    {6, 0, 1, 1, 1, true},
	"HTTL":          {5, 0, 1, 1, 1, false},
	"HPTTL":         {5, 0, 1, 1, 1, false},
	"HEXPIRETIME":   {5, 0, 1, 1, 1, false},
	"HPEXPIRETIME":  {5, 0, 1, 1, 1, false},
	"HPERSIST":      {5, 0, 1, 1, 1, true},
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
// not keep growing indefinitely. It also intercepts HASH RENAME/RENAMENX so the
// datatype is preserved instead of being republished as a string.
func (s *Server) executeCommand(args [][]byte) ([]byte, error) {
	if len(args) > 0 {
		cmd := strings.ToUpper(string(args[0]))
		if (cmd == "RENAME" || cmd == "RENAMENX") && len(args) == 3 {
			handled, renamed, err := s.store.RenameHash(string(args[1]), string(args[2]), cmd == "RENAMENX")
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
	case "HSET", "HMSET":
		if len(args)%2 != 0 {
			return nil, fmt.Errorf("ERR wrong number of arguments for '%s' command", strings.ToLower(cmd))
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
		if cmd == "HMSET" {
			return []byte("+OK\r\n"), nil
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

	case "HRANDFIELD":
		return s.executeHRandField(args)

	case "HEXPIRE", "HPEXPIRE", "HEXPIREAT", "HPEXPIREAT":
		when, fields, err := parseHashFieldExpireArgs(cmd, args)
		if err != nil {
			return nil, err
		}
		results, err := s.store.HashFieldExpireAt(key, fields, when)
		if err != nil {
			return nil, err
		}
		items := make([][]byte, 0, len(results))
		for _, result := range results {
			items = append(items, integer(result))
		}
		return array(items...), nil

	case "HTTL", "HPTTL", "HEXPIRETIME", "HPEXPIRETIME":
		fields, err := parseHashFieldListArgs(args, 2)
		if err != nil {
			return nil, err
		}
		var results []int64
		switch cmd {
		case "HTTL", "HPTTL":
			results, err = s.store.HashFieldPTTL(key, fields)
		default:
			results, err = s.store.HashFieldExpireTime(key, fields)
		}
		if err != nil {
			return nil, err
		}
		items := make([][]byte, 0, len(results))
		for _, result := range results {
			if result >= 0 {
				if cmd == "HTTL" {
					result /= 1000
				} else if cmd == "HEXPIRETIME" {
					result /= 1000
				}
			}
			items = append(items, integer(result))
		}
		return array(items...), nil

	case "HPERSIST":
		fields, err := parseHashFieldListArgs(args, 2)
		if err != nil {
			return nil, err
		}
		results, err := s.store.HashFieldPersist(key, fields)
		if err != nil {
			return nil, err
		}
		items := make([][]byte, 0, len(results))
		for _, result := range results {
			items = append(items, integer(result))
		}
		return array(items...), nil

	case "HSCAN":
		cursor, err := strconv.ParseUint(string(args[2]), 10, 64)
		if err != nil {
			return nil, errors.New("ERR invalid cursor")
		}

		count := 10
		var pattern []byte
		noValues := false

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

			case "NOVALUES":
				noValues = true
				i++

			default:
				return nil, errors.New("ERR syntax error")
			}
		}

		next, pairs, err := s.store.HashScan(key, cursor, count, pattern)
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


func parseHashFieldListArgs(args [][]byte, fieldsPos int) ([][]byte, error) {
	if len(args) <= fieldsPos+1 || !strings.EqualFold(string(args[fieldsPos]), "FIELDS") {
		return nil, errors.New("ERR Mandatory argument FIELDS is missing or not at the right position")
	}
	count, err := strconv.Atoi(string(args[fieldsPos+1]))
	if err != nil || count <= 0 {
		return nil, errors.New("ERR Number of fields must be a positive integer")
	}
	if count != len(args)-(fieldsPos+2) {
		return nil, errors.New("ERR The `numfields` parameter must match the number of arguments")
	}
	return args[fieldsPos+2:], nil
}

func checkedAddInt64(a, b int64) (int64, bool) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, false
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, false
	}
	return a + b, true
}

func checkedMul1000(value int64) (int64, bool) {
	if value > math.MaxInt64/1000 || value < math.MinInt64/1000 {
		return 0, false
	}
	return value * 1000, true
}

func parseHashFieldExpireArgs(cmd string, args [][]byte) (int64, [][]byte, error) {
	raw, err := strconv.ParseInt(string(args[2]), 10, 64)
	if err != nil {
		return 0, nil, errors.New("ERR value is not an integer or out of range")
	}

	fieldsPos := 3
	if fieldsPos < len(args) {
		switch strings.ToUpper(string(args[fieldsPos])) {
		case "NX", "XX", "GT", "LT":
			return 0, nil, errors.New("ERR conditional hash field expiry options are not supported yet")
		}
	}
	fields, err := parseHashFieldListArgs(args, fieldsPos)
	if err != nil {
		return 0, nil, err
	}

	nowMS := time.Now().UnixMilli()
	switch cmd {
	case "HEXPIRE":
		delta, ok := checkedMul1000(raw)
		if !ok {
			return 0, nil, errors.New("ERR invalid expire time in 'hexpire' command")
		}
		whenMS, ok := checkedAddInt64(nowMS, delta)
		if !ok {
			return 0, nil, errors.New("ERR invalid expire time in 'hexpire' command")
		}
		return whenMS, fields, nil

	case "HPEXPIRE":
		whenMS, ok := checkedAddInt64(nowMS, raw)
		if !ok {
			return 0, nil, errors.New("ERR invalid expire time in 'hpexpire' command")
		}
		return whenMS, fields, nil

	case "HEXPIREAT":
		whenMS, ok := checkedMul1000(raw)
		if !ok {
			return 0, nil, errors.New("ERR invalid expire time in 'hexpireat' command")
		}
		return whenMS, fields, nil

	case "HPEXPIREAT":
		return raw, fields, nil

	default:
		return 0, nil, errors.New("ERR unsupported hash field expiry command")
	}
}
