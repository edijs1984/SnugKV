package server

import (
	"errors"
	"strconv"
	"strings"
)

type scanOptions struct {
	Count      int
	Pattern    []byte
	Type       string
	TypeSet    bool
	NoValues   bool
}

// parseScanOptions parses the common MATCH/COUNT surface plus the command-
// specific SCAN TYPE and HSCAN NOVALUES options. Pattern intentionally uses nil
// to mean "MATCH omitted", preserving the distinction from MATCH "".
func parseScanOptions(args [][]byte, allowType, allowNoValues bool) (scanOptions, error) {
	options := scanOptions{Count: 10}
	seenMatch := false
	seenCount := false
	seenType := false
	seenNoValues := false

	for i := 0; i < len(args); {
		switch strings.ToUpper(string(args[i])) {
		case "MATCH":
			if seenMatch || i+1 >= len(args) {
				return scanOptions{}, errors.New("ERR syntax error")
			}
			options.Pattern = args[i+1]
			seenMatch = true
			i += 2

		case "COUNT":
			if seenCount || i+1 >= len(args) {
				return scanOptions{}, errors.New("ERR syntax error")
			}
			count64, err := strconv.ParseInt(string(args[i+1]), 10, 64)
			if err != nil || count64 <= 0 || int64(int(count64)) != count64 {
				return scanOptions{}, errors.New("ERR syntax error")
			}
			options.Count = int(count64)
			seenCount = true
			i += 2

		case "TYPE":
			if !allowType || seenType || i+1 >= len(args) {
				return scanOptions{}, errors.New("ERR syntax error")
			}
			options.Type = strings.ToLower(string(args[i+1]))
			options.TypeSet = true
			seenType = true
			i += 2

		case "NOVALUES":
			if !allowNoValues || seenNoValues {
				return scanOptions{}, errors.New("ERR syntax error")
			}
			options.NoValues = true
			seenNoValues = true
			i++

		default:
			return scanOptions{}, errors.New("ERR syntax error")
		}
	}

	return options, nil
}
