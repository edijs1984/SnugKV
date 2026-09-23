package server

import (
	"errors"
	"snugkv/internal/engine"
	"strconv"
	"strings"
)

var cuckooCommands = map[string]commandInfo{
	"CF.RESERVE":  {3, 0, 1, 1, 1, true},
	"CF.ADD":      {3, 3, 1, 1, 1, true},
	"CF.ADDNX":    {3, 3, 1, 1, 1, true},
	"CF.EXISTS":   {3, 3, 1, 1, 1, false},
	"CF.MEXISTS":  {3, 0, 1, 1, 1, false},
	"CF.COUNT":    {3, 3, 1, 1, 1, false},
	"CF.DEL":      {3, 3, 1, 1, 1, true},
	"CF.INSERT":   {4, 0, 1, 1, 1, true},
	"CF.INSERTNX": {4, 0, 1, 1, 1, true},
	"CF.INFO":     {2, 2, 1, 1, 1, false},
}

func init() {
	for name, info := range cuckooCommands {
		commandTable[name] = info
	}
}

func isCuckooCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := cuckooCommands[strings.ToUpper(string(args[0]))]
	return ok
}

func cuckooBoolArray(values []bool) []byte {
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

func parseCuckooInsert(args [][]byte) (uint64, [][]byte, error) {
	var capacity uint64
	itemsAt := -1
	for pos := 2; pos < len(args); {
		switch strings.ToUpper(string(args[pos])) {
		case "CAPACITY":
			if pos+1 >= len(args) {
				return 0, nil, errors.New("ERR syntax error")
			}
			value, err := strconv.ParseUint(string(args[pos+1]), 10, 64)
			if err != nil {
				return 0, nil, errors.New("Capacity must be in the range [2 * BUCKETSIZE, 1073741824]")
			}
			capacity = value
			pos += 2
		case "ITEMS":
			itemsAt = pos + 1
			pos = len(args)
		default:
			return 0, nil, errors.New("ERR syntax error")
		}
	}
	if itemsAt < 0 || itemsAt >= len(args) {
		return 0, nil, errors.New("ERR syntax error")
	}
	return capacity, args[itemsAt:], nil
}

func (s *Server) executeCuckoo(args [][]byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("ERR empty command")
	}
	cmd := strings.ToUpper(string(args[0]))
	info, ok := cuckooCommands[cmd]
	if !ok {
		return nil, errors.New("ERR unknown Cuckoo command")
	}
	if len(args) < info.min || info.max > 0 && len(args) > info.max {
		return nil, errors.New("ERR wrong number of arguments for '" + strings.ToLower(cmd) + "' command")
	}

	key := string(args[1])
	switch cmd {
	case "CF.RESERVE":
		if len(args) != 3 {
			return nil, errors.New("ERR syntax error")
		}
		capacity, err := strconv.ParseInt(string(args[2]), 10, 64)
		if err != nil || capacity < 0 {
			return nil, errors.New("Capacity must be in the range [2 * BUCKETSIZE, 1073741824]")
		}
		if err := s.store.CuckooReserve(key, uint64(capacity)); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil

	case "CF.ADD", "CF.ADDNX":
		added, err := s.store.CuckooAdd(key, args[2], cmd == "CF.ADDNX")
		if err != nil {
			return nil, err
		}
		if added {
			return integer(1), nil
		}
		return integer(0), nil

	case "CF.EXISTS":
		found, err := s.store.CuckooExists(key, args[2])
		if err != nil {
			return nil, err
		}
		if found {
			return integer(1), nil
		}
		return integer(0), nil

	case "CF.MEXISTS":
		results := make([]bool, len(args)-2)
		for i := 2; i < len(args); i++ {
			found, err := s.store.CuckooExists(key, args[i])
			if err != nil {
				return nil, err
			}
			results[i-2] = found
		}
		return cuckooBoolArray(results), nil

	case "CF.COUNT":
		count, err := s.store.CuckooCount(key, args[2])
		if err != nil {
			return nil, err
		}
		return integer(int64(count)), nil

	case "CF.DEL":
		deleted, err := s.store.CuckooDelete(key, args[2])
		if err != nil {
			return nil, err
		}
		if deleted {
			return integer(1), nil
		}
		return integer(0), nil

	case "CF.INSERT", "CF.INSERTNX":
		capacity, items, err := parseCuckooInsert(args)
		if err != nil {
			return nil, err
		}
		results, err := s.store.CuckooInsert(key, capacity, items, cmd == "CF.INSERTNX")
		if err != nil {
			return nil, err
		}
		return cuckooBoolArray(results), nil

	case "CF.INFO":
		info, err := s.store.CuckooInfo(key)
		if err != nil {
			return nil, err
		}
		return array(
			[]byte("+Size\r\n"), integer(int64(info.Size)),
			[]byte("+Number of buckets\r\n"), integer(int64(info.NumBuckets)),
			[]byte("+Number of filters\r\n"), integer(int64(info.NumFilters)),
			[]byte("+Number of items inserted\r\n"), integer(int64(info.NumItems)),
			[]byte("+Number of items deleted\r\n"), integer(int64(info.NumDeletes)),
			[]byte("+Bucket size\r\n"), integer(int64(info.BucketSize)),
			[]byte("+Expansion rate\r\n"), integer(int64(info.Expansion)),
			[]byte("+Max iterations\r\n"), integer(int64(info.MaxIterations)),
		), nil
	}
	return nil, errors.New("ERR unknown Cuckoo command")
}

var _ = engine.TypeCuckoo
