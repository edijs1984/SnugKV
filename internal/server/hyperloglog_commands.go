package server

import (
	"errors"
	"strings"
)

var hyperLogLogCommands = map[string]commandInfo{
	"PFADD":   {2, 0, 1, 1, 1, true},
	"PFCOUNT": {2, 0, 1, -1, 1, false},
	"PFMERGE": {2, 0, 1, -1, 1, true},
}

func init() {
	for name, info := range hyperLogLogCommands {
		commandTable[name] = info
	}
}

func isHyperLogLogCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := hyperLogLogCommands[strings.ToUpper(string(args[0]))]
	return ok
}

func (s *Server) executeHyperLogLog(args [][]byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("ERR empty command")
	}
	cmd := strings.ToUpper(string(args[0]))
	info, ok := hyperLogLogCommands[cmd]
	if !ok {
		return nil, errors.New("ERR unknown HyperLogLog command")
	}
	if len(args) < info.min || info.max > 0 && len(args) > info.max {
		return nil, errors.New("ERR wrong number of arguments for '" + strings.ToLower(cmd) + "' command")
	}

	switch cmd {
	case "PFADD":
		changed, err := s.store.HLLAdd(string(args[1]), args[2:])
		if err != nil {
			return nil, err
		}
		if changed {
			return integer(1), nil
		}
		return integer(0), nil

	case "PFCOUNT":
		keys := make([]string, len(args)-1)
		for i := 1; i < len(args); i++ {
			keys[i-1] = string(args[i])
		}
		cardinality, err := s.store.HLLCount(keys)
		if err != nil {
			return nil, err
		}
		return integer(int64(cardinality)), nil

	case "PFMERGE":
		sources := make([]string, 0, len(args)-2)
		for _, arg := range args[2:] {
			sources = append(sources, string(arg))
		}
		if err := s.store.HLLMerge(string(args[1]), sources); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil
	}

	return nil, errors.New("ERR unknown HyperLogLog command")
}
