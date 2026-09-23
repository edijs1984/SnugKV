package server

import (
	"errors"
	"snugkv/internal/engine"
	"strconv"
	"strings"
)

var bloomCommands = map[string]commandInfo{
	"BF.RESERVE": {4, 6, 1, 1, 1, true},
	"BF.ADD":     {3, 3, 1, 1, 1, true},
	"BF.EXISTS":  {3, 3, 1, 1, 1, false},
	"BF.MADD":    {3, 0, 1, 1, 1, true},
	"BF.MEXISTS": {3, 0, 1, 1, 1, false},
	"BF.CARD":    {2, 2, 1, 1, 1, false},
	"BF.INFO":    {2, 3, 1, 1, 1, false},
	"BF.INSERT":  {4, 0, 1, 1, 1, true},
}

func init() {
	for name, info := range bloomCommands {
		commandTable[name] = info
	}
}

func isBloomCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := bloomCommands[strings.ToUpper(string(args[0]))]
	return ok
}

func bloomBoolArray(values []bool) []byte {
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

func bloomInsertArray(values []engine.BloomInsertResult) []byte {
	items := make([][]byte, len(values))
	for i, value := range values {
		if value.Err != nil {
			items[i] = errorResponse(value.Err)
		} else if value.Added {
			items[i] = integer(1)
		} else {
			items[i] = integer(0)
		}
	}
	return array(items...)
}

func (s *Server) executeBloom(args [][]byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("ERR empty command")
	}
	cmd := strings.ToUpper(string(args[0]))
	info, ok := bloomCommands[cmd]
	if !ok {
		return nil, errors.New("ERR unknown Bloom command")
	}
	if len(args) < info.min || info.max > 0 && len(args) > info.max {
		return nil, errors.New("ERR wrong number of arguments for '" + strings.ToLower(cmd) + "' command")
	}

	key := string(args[1])
	switch cmd {
	case "BF.RESERVE":
		errorRate, err := strconv.ParseFloat(string(args[2]), 64)
		if err != nil {
			return nil, errors.New("ERR error rate must be in the range (0.000000, 1.000000)")
		}
		capacity, err := strconv.ParseUint(string(args[3]), 10, 64)
		if err != nil {
			return nil, errors.New("ERR capacity must be in the range [1, 1073741824]")
		}
		options := engine.BloomOptions{Expansion: 2}
		if len(args) == 5 {
			// RedisBloom accepts a lone fifth token as the non-scaling form;
			// the audited module also accepts an otherwise unknown token here.
			options = engine.BloomOptions{NonScaling: true}
		} else if len(args) == 6 {
			if !strings.EqualFold(string(args[4]), "EXPANSION") {
				return nil, errors.New("ERR syntax error")
			}
			expansion, err := strconv.ParseInt(string(args[5]), 10, 64)
			if err != nil || expansion < 0 || expansion > 32768 {
				return nil, errors.New("ERR expansion must be in the range [0, 32768]")
			}
			if expansion == 0 {
				options = engine.BloomOptions{NonScaling: true}
			} else {
				options = engine.BloomOptions{Expansion: uint32(expansion)}
			}
		}
		if err := s.store.BloomReserveWithOptions(key, errorRate, capacity, options); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil

	case "BF.ADD":
		added, err := s.store.BloomAdd(key, args[2])
		if err != nil {
			return nil, err
		}
		if added {
			return integer(1), nil
		}
		return integer(0), nil

	case "BF.EXISTS":
		found, err := s.store.BloomExists(key, args[2])
		if err != nil {
			return nil, err
		}
		if found {
			return integer(1), nil
		}
		return integer(0), nil

	case "BF.MADD":
		results, err := s.store.BloomInsert(key, 0, 0, args[2:])
		if err != nil {
			return nil, err
		}
		return bloomBoolArray(results), nil

	case "BF.MEXISTS":
		results := make([]bool, len(args)-2)
		for i := 2; i < len(args); i++ {
			found, err := s.store.BloomExists(key, args[i])
			if err != nil {
				return nil, err
			}
			results[i-2] = found
		}
		return bloomBoolArray(results), nil

	case "BF.CARD":
		count, err := s.store.BloomCard(key)
		if err != nil {
			return nil, err
		}
		return integer(int64(count)), nil

	case "BF.INFO":
		info, err := s.store.BloomInfo(key)
		if err != nil {
			return nil, err
		}
		if len(args) == 3 {
			switch strings.ToUpper(string(args[2])) {
			case "CAPACITY":
				return array(integer(int64(info.Capacity))), nil
			case "SIZE":
				return array(integer(int64(info.Size))), nil
			case "FILTERS":
				return array(integer(int64(info.Filters))), nil
			case "ITEMS":
				return array(integer(int64(info.Items))), nil
			case "EXPANSION":
				if !info.Scaling {
					return array(nullBulk()), nil
				}
				return array(integer(int64(info.Expansion))), nil
			default:
				return nil, errors.New("ERR Invalid information value")
			}
		}
		return array(
			[]byte("+Capacity\r\n"), integer(int64(info.Capacity)),
			[]byte("+Size\r\n"), integer(int64(info.Size)),
			[]byte("+Number of filters\r\n"), integer(int64(info.Filters)),
			[]byte("+Number of items inserted\r\n"), integer(int64(info.Items)),
			[]byte("+Expansion rate\r\n"), func() []byte {
				if !info.Scaling {
					return nullBulk()
				}
				return integer(int64(info.Expansion))
			}(),
		), nil

	case "BF.INSERT":
		var capacity uint64
		var errorRate float64
		options := engine.BloomOptions{Expansion: 2}
		itemsAt := -1
		for pos := 2; pos < len(args); {
			switch strings.ToUpper(string(args[pos])) {
			case "CAPACITY":
				if pos+1 >= len(args) {
					return nil, errors.New("ERR syntax error")
				}
				v, err := strconv.ParseUint(string(args[pos+1]), 10, 64)
				if err != nil {
					return nil, errors.New("ERR capacity must be in the range [1, 1073741824]")
				}
				capacity = v
				pos += 2
			case "ERROR":
				if pos+1 >= len(args) {
					return nil, errors.New("ERR syntax error")
				}
				v, err := strconv.ParseFloat(string(args[pos+1]), 64)
				if err != nil {
					return nil, errors.New("ERR error rate must be in the range (0.000000, 1.000000)")
				}
				errorRate = v
				pos += 2
			case "EXPANSION":
				if pos+1 >= len(args) {
					return nil, errors.New("ERR syntax error")
				}
				v, err := strconv.ParseInt(string(args[pos+1]), 10, 64)
				if err != nil || v < 0 || v > 32768 {
					return nil, errors.New("ERR expansion must be in the range [0, 32768]")
				}
				if v == 0 {
					options = engine.BloomOptions{NonScaling: true}
				} else {
					options.Expansion = uint32(v)
				}
				pos += 2
			case "NONSCALING":
				options.NonScaling = true
				pos++
			case "ITEMS":
				itemsAt = pos + 1
				pos = len(args)
			default:
				return nil, errors.New("ERR syntax error")
			}
		}
		if itemsAt < 0 || itemsAt >= len(args) {
			return nil, errors.New("ERR syntax error")
		}
		results, err := s.store.BloomInsertWithOptions(key, capacity, errorRate, options, args[itemsAt:])
		if err != nil {
			return nil, err
		}
		return bloomInsertArray(results), nil
	}
	return nil, errors.New("ERR unknown Bloom command")
}
