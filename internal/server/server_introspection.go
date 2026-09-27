package server

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

func (s *Server) executeObject(args [][]byte) ([]byte, error) {
	if len(args) < 2 {
		return nil, errors.New("ERR wrong number of arguments for 'object' command")
	}

	sub := strings.ToUpper(string(args[1]))
	switch sub {
	case "HELP":
		if len(args) != 2 {
			return nil, errors.New("ERR wrong number of arguments for 'object|help' command")
		}
		return array(
			formatBulkString([]byte("ENCODING <key> - Return the kind of internal representation used in order to store the value associated with a key.")),
			formatBulkString([]byte("FREQ <key> - Return the logarithmic access frequency counter of a Redis object.")),
			formatBulkString([]byte("IDLETIME <key> - Return the idle time of a Redis object.")),
			formatBulkString([]byte("REFCOUNT <key> - Return the number of references of the value associated with the specified key.")),
			formatBulkString([]byte("HELP - Print this help.")),
		), nil

	case "ENCODING":
		if len(args) != 3 {
			return nil, errors.New("ERR wrong number of arguments for 'object|encoding' command")
		}
		name, _, _, found := s.store.Encoding(string(args[2]))
		if !found {
			return nullBulk(), nil
		}
		return formatBulkString([]byte(name)), nil

	case "REFCOUNT":
		if len(args) != 3 {
			return nil, errors.New("ERR wrong number of arguments for 'object|refcount' command")
		}
		_, found := s.store.ValueTypeOf(string(args[2]))
		if !found {
			return nullBulk(), nil
		}
		// SnugKV does not expose RedisObject-style shared references. Each key
		// owns one logical value entry, so the compatible logical refcount is 1.
		return integer(1), nil

	case "IDLETIME", "FREQ":
		return nil, errors.New("ERR OBJECT subcommand requires access metadata tracking")

	default:
		return nil, errors.New("ERR unknown subcommand")
	}
}

func timeReply(now time.Time) []byte {
	sec := strconv.FormatInt(now.Unix(), 10)
	usec := strconv.FormatInt(int64(now.Nanosecond()/1000), 10)
	return array(
		formatBulkString([]byte(sec)),
		formatBulkString([]byte(usec)),
	)
}
