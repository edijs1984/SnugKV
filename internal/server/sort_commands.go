package server

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"snugkv/internal/engine"
)

var sortCommands = map[string]commandInfo{
	"SORT":    {2, 0, 1, 1, 1, true},
	"SORT_RO": {2, 0, 1, 1, 1, false},
}

func init() {
	for name, info := range sortCommands {
		commandTable[name] = info
	}
}

func isSortCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := sortCommands[strings.ToUpper(string(args[0]))]
	return ok
}

type sortOptions struct {
	by        []byte
	hasBy     bool
	limit     bool
	offset    int64
	count     int64
	get       [][]byte
	desc      bool
	alpha     bool
	store     string
	hasStore  bool
}

type sortItem struct {
	value []byte
	num   float64
	alpha []byte
}

func parseSortOptions(args [][]byte, readOnly bool) (sortOptions, error) {
	options := sortOptions{count: -1}
	for i := 0; i < len(args); {
		switch strings.ToUpper(string(args[i])) {
		case "BY":
			if i+1 >= len(args) {
				return sortOptions{}, errors.New("ERR syntax error")
			}
			options.by = append([]byte(nil), args[i+1]...)
			options.hasBy = true
			i += 2
		case "LIMIT":
			if i+2 >= len(args) {
				return sortOptions{}, errors.New("ERR syntax error")
			}
			offset, err := strconv.ParseInt(string(args[i+1]), 10, 64)
			if err != nil {
				return sortOptions{}, errors.New("ERR value is not an integer or out of range")
			}
			count, err := strconv.ParseInt(string(args[i+2]), 10, 64)
			if err != nil {
				return sortOptions{}, errors.New("ERR value is not an integer or out of range")
			}
			options.limit = true
			options.offset = offset
			options.count = count
			i += 3
		case "GET":
			if i+1 >= len(args) {
				return sortOptions{}, errors.New("ERR syntax error")
			}
			options.get = append(options.get, append([]byte(nil), args[i+1]...))
			i += 2
		case "ASC":
			options.desc = false
			i++
		case "DESC":
			options.desc = true
			i++
		case "ALPHA":
			options.alpha = true
			i++
		case "STORE":
			if readOnly || i+1 >= len(args) {
				return sortOptions{}, errors.New("ERR syntax error")
			}
			options.store = string(args[i+1])
			options.hasStore = true
			i += 2
		default:
			return sortOptions{}, errors.New("ERR syntax error")
		}
	}
	return options, nil
}

func sortStoreDestination(args [][]byte) (string, bool) {
	if len(args) < 2 || !strings.EqualFold(string(args[0]), "SORT") {
		return "", false
	}
	options, err := parseSortOptions(args[2:], false)
	if err != nil || !options.hasStore {
		return "", false
	}
	return options.store, true
}

func (s *Server) sortSourceElements(key string) ([][]byte, error) {
	t, ok := s.store.ValueTypeOf(key)
	if !ok {
		return nil, nil
	}
	switch t {
	case engine.TypeList:
		return s.store.ListRange(key, 0, -1)
	case engine.TypeSet:
		return s.store.SetMembers(key)
	case engine.TypeZSet:
		items, err := s.store.ZSetRange(key, 0, -1, false)
		if err != nil {
			return nil, err
		}
		out := make([][]byte, len(items))
		for i, item := range items {
			out[i] = append([]byte(nil), item.Member...)
		}
		return out, nil
	default:
		return nil, errWrongType
	}
}

func sortExternalStringType(t engine.ValueType) bool {
	switch t {
	case engine.TypeHash, engine.TypeSet, engine.TypeList, engine.TypeZSet, engine.TypeStream:
		return false
	default:
		return true
	}
}

// lookupSortPattern implements Redis SORT's external-key substitution. Only the
// first '*' is substituted. A hash dereference uses key-pattern->field. Patterns
// without '*' return no value; GET # is the special identity form.
func (s *Server) lookupSortPattern(pattern, subst []byte) ([]byte, bool, error) {
	if bytes.Equal(pattern, []byte("#")) {
		return append([]byte(nil), subst...), true, nil
	}
	star := bytes.IndexByte(pattern, '*')
	if star < 0 {
		return nil, false, nil
	}

	keyEnd := len(pattern)
	var field []byte
	if rest := bytes.Index(pattern[star+1:], []byte("->")); rest >= 0 {
		arrow := star + 1 + rest
		if arrow+2 < len(pattern) {
			keyEnd = arrow
			field = pattern[arrow+2:]
		}
	}
	keyBytes := make([]byte, 0, keyEnd-1+len(subst))
	keyBytes = append(keyBytes, pattern[:star]...)
	keyBytes = append(keyBytes, subst...)
	keyBytes = append(keyBytes, pattern[star+1:keyEnd]...)
	key := string(keyBytes)

	if field != nil {
		t, ok := s.store.ValueTypeOf(key)
		if !ok || t != engine.TypeHash {
			return nil, false, nil
		}
		return s.store.HashGet(key, field)
	}

	t, ok := s.store.ValueTypeOf(key)
	if !ok || !sortExternalStringType(t) {
		return nil, false, nil
	}
	value, found := s.store.Get(key)
	return value, found, nil
}

func sortNumber(value []byte) (float64, error) {
	if len(value) == 0 {
		return 0, errors.New("ERR One or more scores can't be converted into double")
	}
	n, err := strconv.ParseFloat(string(value), 64)
	if err != nil || math.IsNaN(n) {
		return 0, errors.New("ERR One or more scores can't be converted into double")
	}
	return n, nil
}

func (s *Server) sortItems(elements [][]byte, options sortOptions) ([]sortItem, bool, error) {
	items := make([]sortItem, len(elements))
	for i, element := range elements {
		items[i].value = append([]byte(nil), element...)
	}

	// Redis treats a BY pattern with no wildcard as BY nosort: preserve the
	// source iteration order and only apply LIMIT / GET processing.
	dontSort := options.hasBy && bytes.IndexByte(options.by, '*') < 0
	if dontSort || len(items) < 2 {
		return items, dontSort, nil
	}

	for i := range items {
		weight := items[i].value
		found := true
		if options.hasBy {
			var err error
			weight, found, err = s.lookupSortPattern(options.by, items[i].value)
			if err != nil {
				return nil, false, err
			}
		}
		if options.alpha {
			if found {
				items[i].alpha = append([]byte(nil), weight...)
			}
			continue
		}
		if !found {
			items[i].num = 0
			continue
		}
		n, err := sortNumber(weight)
		if err != nil {
			return nil, false, err
		}
		items[i].num = n
	}

	sort.Slice(items, func(i, j int) bool {
		cmp := 0
		if options.alpha {
			cmp = bytes.Compare(items[i].alpha, items[j].alpha)
		} else {
			if items[i].num < items[j].num {
				cmp = -1
			} else if items[i].num > items[j].num {
				cmp = 1
			}
		}
		if cmp == 0 {
			cmp = bytes.Compare(items[i].value, items[j].value)
		}
		if options.desc {
			return cmp > 0
		}
		return cmp < 0
	})
	return items, false, nil
}

func sortLimit(items []sortItem, options sortOptions) []sortItem {
	if !options.limit {
		return items
	}
	start := options.offset
	if start < 0 {
		start = 0
	}
	if start >= int64(len(items)) || options.count == 0 {
		return nil
	}
	if options.count < 0 {
		return items[start:]
	}
	end := start + options.count
	if end < start || end > int64(len(items)) {
		end = int64(len(items))
	}
	return items[start:end]
}

func (s *Server) sortOutput(items []sortItem, options sortOptions) (wire [][]byte, stored [][]byte, err error) {
	if len(options.get) == 0 {
		wire = make([][]byte, 0, len(items))
		stored = make([][]byte, 0, len(items))
		for _, item := range items {
			wire = append(wire, formatBulkString(item.value))
			stored = append(stored, append([]byte(nil), item.value...))
		}
		return wire, stored, nil
	}

	wire = make([][]byte, 0, len(items)*len(options.get))
	stored = make([][]byte, 0, len(items)*len(options.get))
	for _, item := range items {
		for _, pattern := range options.get {
			value, found, lookupErr := s.lookupSortPattern(pattern, item.value)
			if lookupErr != nil {
				return nil, nil, lookupErr
			}
			wire = append(wire, optionalBulk(value, found))
			if found {
				stored = append(stored, append([]byte(nil), value...))
			} else {
				// Redis stores missing GET results as empty list elements.
				stored = append(stored, []byte{})
			}
		}
	}
	return wire, stored, nil
}

func (s *Server) executeSort(args [][]byte) ([]byte, error) {
	if len(args) < 2 {
		cmd := "sort"
		if len(args) > 0 {
			cmd = strings.ToLower(string(args[0]))
		}
		return nil, fmt.Errorf("ERR wrong number of arguments for '%s' command", cmd)
	}
	cmd := strings.ToUpper(string(args[0]))
	readOnly := cmd == "SORT_RO"
	options, err := parseSortOptions(args[2:], readOnly)
	if err != nil {
		return nil, err
	}

	elements, err := s.sortSourceElements(string(args[1]))
	if err != nil {
		return nil, err
	}
	items, _, err := s.sortItems(elements, options)
	if err != nil {
		return nil, err
	}
	items = sortLimit(items, options)
	wire, stored, err := s.sortOutput(items, options)
	if err != nil {
		return nil, err
	}
	if options.hasStore {
		if err := s.store.ListReplace(options.store, stored); err != nil {
			return nil, err
		}
		return integer(int64(len(stored))), nil
	}
	return array(wire...), nil
}

// sortPressureKeys protects the logical inputs and STORE destination across an
// OOM eviction retry. External BY/GET keys are derived from the current source
// elements so a retry cannot observe a different lookup set after eviction.
func (s *Server) sortPressureKeys(args [][]byte) []string {
	if len(args) < 2 {
		return nil
	}
	keys := map[string]struct{}{string(args[1]): {}}
	options, err := parseSortOptions(args[2:], strings.EqualFold(string(args[0]), "SORT_RO"))
	if err != nil {
		return []string{string(args[1])}
	}
	if options.hasStore {
		keys[options.store] = struct{}{}
	}
	elements, err := s.sortSourceElements(string(args[1]))
	if err == nil {
		patterns := make([][]byte, 0, 1+len(options.get))
		if options.hasBy {
			patterns = append(patterns, options.by)
		}
		patterns = append(patterns, options.get...)
		for _, pattern := range patterns {
			if bytes.Equal(pattern, []byte("#")) {
				continue
			}
			star := bytes.IndexByte(pattern, '*')
			if star < 0 {
				continue
			}
			keyEnd := len(pattern)
			if rest := bytes.Index(pattern[star+1:], []byte("->")); rest >= 0 {
				arrow := star + 1 + rest
				if arrow+2 < len(pattern) {
					keyEnd = arrow
				}
			}
			for _, element := range elements {
				resolved := make([]byte, 0, keyEnd-1+len(element))
				resolved = append(resolved, pattern[:star]...)
				resolved = append(resolved, element...)
				resolved = append(resolved, pattern[star+1:keyEnd]...)
				keys[string(resolved)] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	return out
}
