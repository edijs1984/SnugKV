package server

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var streamGroupCommands = map[string]commandInfo{
	"XGROUP": {4, 0, 2, 2, 1, true},
}

func init() {
	for name, info := range streamGroupCommands {
		commandTable[name] = info
	}
}

func isStreamGroupCommand(args [][]byte) bool {
	return len(args) > 0 && strings.EqualFold(string(args[0]), "XGROUP")
}

func parseEntriesReadOption(args [][]byte, start int) (*int64, int, error) {
	if start >= len(args) {
		return nil, start, nil
	}
	if !strings.EqualFold(string(args[start]), "ENTRIESREAD") || start+1 >= len(args) {
		return nil, start, errors.New("ERR syntax error")
	}
	value, err := strconv.ParseInt(string(args[start+1]), 10, 64)
	if err != nil || value < -1 {
		return nil, start, errors.New("ERR value is not an integer or out of range")
	}
	return &value, start + 2, nil
}

func (s *Server) executeStreamGroup(args [][]byte) ([]byte, error) {
	if len(args) < 2 {
		return nil, errors.New("ERR wrong number of arguments for 'xgroup' command")
	}
	sub := strings.ToUpper(string(args[1]))
	switch sub {
	case "CREATE":
		if len(args) < 5 {
			return nil, errors.New("ERR wrong number of arguments for 'xgroup|create' command")
		}
		key, group, idSpec := string(args[2]), string(args[3]), string(args[4])
		mkstream := false
		entriesRead := int64(-1)
		for i := 5; i < len(args); {
			switch strings.ToUpper(string(args[i])) {
			case "MKSTREAM":
				if mkstream {
					return nil, errors.New("ERR syntax error")
				}
				mkstream = true
				i++
			case "ENTRIESREAD":
				parsed, next, err := parseEntriesReadOption(args, i)
				if err != nil {
					return nil, err
				}
				if entriesRead != -1 {
					return nil, errors.New("ERR syntax error")
				}
				entriesRead = *parsed
				i = next
			default:
				return nil, errors.New("ERR syntax error")
			}
		}
		if err := s.store.StreamGroupCreate(key, group, idSpec, mkstream, entriesRead); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil

	case "DESTROY":
		if len(args) != 4 {
			return nil, errors.New("ERR wrong number of arguments for 'xgroup|destroy' command")
		}
		deleted, err := s.store.StreamGroupDestroy(string(args[2]), string(args[3]))
		if err != nil {
			return nil, err
		}
		return integer(deleted), nil

	case "SETID":
		if len(args) < 5 {
			return nil, errors.New("ERR wrong number of arguments for 'xgroup|setid' command")
		}
		var entriesRead *int64
		if len(args) > 5 {
			parsed, next, err := parseEntriesReadOption(args, 5)
			if err != nil || next != len(args) {
				if err != nil {
					return nil, err
				}
				return nil, errors.New("ERR syntax error")
			}
			entriesRead = parsed
		}
		if err := s.store.StreamGroupSetID(string(args[2]), string(args[3]), string(args[4]), entriesRead); err != nil {
			return nil, err
		}
		return []byte("+OK\r\n"), nil

	case "CREATECONSUMER":
		if len(args) != 5 {
			return nil, errors.New("ERR wrong number of arguments for 'xgroup|createconsumer' command")
		}
		created, err := s.store.StreamGroupCreateConsumer(string(args[2]), string(args[3]), string(args[4]))
		if err != nil {
			return nil, err
		}
		return integer(created), nil

	case "DELCONSUMER":
		if len(args) != 5 {
			return nil, errors.New("ERR wrong number of arguments for 'xgroup|delconsumer' command")
		}
		pending, err := s.store.StreamGroupDeleteConsumer(string(args[2]), string(args[3]), string(args[4]))
		if err != nil {
			return nil, err
		}
		return integer(pending), nil
	default:
		return nil, fmt.Errorf("ERR unknown subcommand or wrong number of arguments for '%s'. Try XGROUP HELP.", strings.ToLower(sub))
	}
}
