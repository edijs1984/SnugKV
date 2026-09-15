package server

import (
	"errors"
	"strconv"
	"strings"
)

func isKeyspaceScanCommand(args [][]byte) bool {
	return len(args) > 0 && strings.EqualFold(string(args[0]), "SCAN")
}

// executeKeyspaceScan owns the modern SCAN option surface. Keeping it outside
// the legacy monolithic switch lets TYPE/MATCH/COUNT share the same parser as
// the aggregate scan commands without changing unrelated command routing.
func (s *Server) executeKeyspaceScan(args [][]byte) ([]byte, error) {
	if len(args) < 2 {
		return nil, errors.New("ERR wrong number of arguments for 'scan' command")
	}

	cursor, err := strconv.ParseUint(string(args[1]), 10, 64)
	if err != nil {
		return nil, errors.New("ERR invalid cursor")
	}

	options, err := parseScanOptions(args[2:], true, false)
	if err != nil {
		return nil, err
	}

	pattern := "*"
	if options.Pattern != nil {
		pattern = string(options.Pattern)
	}
	typeFilter := ""
	if options.TypeSet {
		typeFilter = options.Type
	}

	next, foundKeys := s.store.ScanTyped(cursor, options.Count, pattern, typeFilter)
	items := make([][]byte, 0, len(foundKeys))
	for _, key := range foundKeys {
		items = append(items, formatBulkString([]byte(key)))
	}

	return array(
		formatBulkString([]byte(strconv.FormatUint(next, 10))),
		array(items...),
	), nil
}
