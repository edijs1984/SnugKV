package server

import (
	"errors"
	"snugkv/internal/engine"
	"strings"
)

var errWrongType = errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")

func isNativeContainerValueType(t engine.ValueType) bool {
	switch t {
	case engine.TypeHash, engine.TypeSet, engine.TypeList, engine.TypeZSet:
		return true
	default:
		return false
	}
}

func (s *Server) nativeContainerKey(key string) bool {
	t, ok := s.store.ValueTypeOf(key)
	return ok && isNativeContainerValueType(t)
}

// validateLegacyScalarTypes protects older scalar/numeric/bitmap paths from
// decoding native container storage as if it were a Redis string.
func (s *Server) validateLegacyScalarTypes(args [][]byte) error {
	if len(args) == 0 {
		return nil
	}
	cmd := strings.ToUpper(string(args[0]))
	var keys []string

	switch cmd {
	case "GET", "GETSET", "GETEX", "APPEND", "GETRANGE", "SETRANGE",
		"GETBIT", "SETBIT", "BITCOUNT", "BITPOS", "INCR", "DECR",
		"INCRBY", "DECRBY", "INCRBYFLOAT", "STRLEN":
		if len(args) > 1 {
			keys = []string{string(args[1])}
		}

	case "SET":
		// Plain SET overwrites any Redis type. Only SET ... GET needs the old
		// value to be a string, and Redis aborts the SET on wrong type.
		for _, arg := range args[3:] {
			if strings.EqualFold(string(arg), "GET") {
				if len(args) > 1 {
					keys = []string{string(args[1])}
				}
				break
			}
		}

	case "BITOP":
		// Destination is allowed to overwrite any existing type. Sources must
		// be string-compatible.
		if len(args) > 3 {
			keys = make([]string, 0, len(args)-3)
			for _, arg := range args[3:] {
				keys = append(keys, string(arg))
			}
		}

	default:
		return nil
	}

	for _, key := range keys {
		if s.nativeContainerKey(key) {
			return errWrongType
		}
	}
	return nil
}

func isTypedScalarSpecial(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	switch strings.ToUpper(string(args[0])) {
	case "MGET", "GETDEL":
		return true
	default:
		return false
	}
}

func (s *Server) executeTypedScalarSpecial(args [][]byte) ([]byte, error) {
	cmd := strings.ToUpper(string(args[0]))
	switch cmd {
	case "MGET":
		if len(args) < 2 {
			return s.executeRoutedCommand(args)
		}
		keys := make([]string, 0, len(args)-1)
		for _, arg := range args[1:] {
			keys = append(keys, string(arg))
		}
		values, found := s.store.MGetStrings(keys)
		items := make([][]byte, 0, len(values))
		for i := range values {
			items = append(items, optionalBulk(values[i], found[i]))
		}
		return array(items...), nil

	case "GETDEL":
		if len(args) != 2 {
			return s.executeRoutedCommand(args)
		}
		value, found := s.store.GetDelString(string(args[1]))
		return optionalBulk(value, found), nil
	}

	return s.executeRoutedCommand(args)
}
