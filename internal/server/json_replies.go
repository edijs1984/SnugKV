package server

import (
	"errors"
	"strings"

	"snugkv/internal/jsonvalue"
)

// RedisJSON answers a JSONPath query ("$...") with one entry per matched value,
// and a legacy path (".a.b") with a single value. These helpers build the
// per-match replies for the JSONPath form.

func isJSONPathArg(path string) bool { return strings.HasPrefix(path, "$") }

// jsonIntegerMatches: an array of integers, null for a match of the wrong type.
func jsonIntegerMatches(results []any) []byte {
	items := make([][]byte, len(results))
	for i, result := range results {
		switch value := result.(type) {
		case int64:
			items[i] = integer(value)
		case bool:
			items[i] = boolean(value)
		default:
			items[i] = nullBulk()
		}
	}
	return array(items...)
}

// jsonNumberMatches: one bulk string holding a JSON array of numbers, as
// JSON.NUMINCRBY and JSON.NUMMULTBY reply to a JSONPath query.
func jsonNumberMatches(results []any) []byte {
	var out strings.Builder
	out.WriteByte('[')
	for i, result := range results {
		if i > 0 {
			out.WriteByte(',')
		}
		number, ok := result.(float64)
		if !ok {
			out.WriteString("null")
			continue
		}
		encoded, err := jsonvalue.Encode(number)
		if err != nil {
			out.WriteString("null")
			continue
		}
		out.Write(encoded)
	}
	out.WriteByte(']')
	return formatBulkString([]byte(out.String()))
}

// jsonEncodedMatches: an array of bulk strings holding encoded JSON values.
func jsonEncodedMatches(results []any) []byte {
	items := make([][]byte, len(results))
	for i, result := range results {
		if encoded, ok := result.([]byte); ok {
			items[i] = formatBulkString(encoded)
		} else {
			items[i] = nullBulk()
		}
	}
	return array(items...)
}

// jsonKeyListMatches: an array with one array of object keys per match.
func jsonKeyListMatches(results []any) []byte {
	items := make([][]byte, len(results))
	for i, result := range results {
		names, ok := result.([]string)
		if !ok {
			items[i] = nullBulk()
			continue
		}
		bulk := make([][]byte, len(names))
		for j, name := range names {
			bulk[j] = formatBulkString([]byte(name))
		}
		items[i] = array(bulk...)
	}
	return array(items...)
}

// jsonGetMultiplePaths answers JSON.GET key path path...: one JSON object that
// maps every path to its result (the matches array for a JSONPath, the value for
// a legacy path).
func (s *Server) jsonGetMultiplePaths(key string, paths [][]byte) ([]byte, error) {
	var out []byte
	out = append(out, '{')
	for i, raw := range paths {
		path := string(raw)
		switch strings.ToUpper(path) {
		case "INDENT", "NEWLINE", "SPACE", "NOESCAPE":
			return nil, errors.New("ERR formatting options are not supported with several paths")
		}
		value, found, err := s.store.JSONGet(key, path)
		if err != nil {
			return nil, err
		}
		if !found {
			if i == 0 {
				return nullBulk(), nil
			}
			return nil, errors.New("ERR Path '" + path + "' does not exist")
		}
		if i > 0 {
			out = append(out, ',')
		}
		name, err := jsonvalue.Encode(path)
		if err != nil {
			return nil, err
		}
		out = append(out, name...)
		out = append(out, ':')
		out = append(out, value...)
	}
	out = append(out, '}')
	return formatBulkString(out), nil
}
