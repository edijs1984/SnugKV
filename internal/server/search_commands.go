package server

import (
	"errors"
	"strconv"
	"strings"

	"snugkv/internal/engine"
)

var searchCommands = map[string]commandInfo{
	"FT.CREATE":    {6, 0, 0, 0, 0, true},
	"FT.DROPINDEX": {2, 2, 0, 0, 0, true},
	"FT._LIST":     {1, 1, 0, 0, 0, false},
}

func init() {
	for name, info := range searchCommands {
		commandTable[name] = info
	}
}

func executeFTCreate(store *engine.Store, args [][]byte) ([]byte, error) {
	if len(args) < 6 {
		return nil, errors.New("ERR wrong number of arguments for 'ft.create' command")
	}

	def := engine.SearchDefinition{
		Name: string(args[1]),
	}

	pos := 2

	if pos >= len(args) || !strings.EqualFold(string(args[pos]), "ON") {
		return nil, errors.New("ERR FT.CREATE requires ON JSON")
	}
	pos++

	if pos >= len(args) || !strings.EqualFold(string(args[pos]), "JSON") {
		return nil, errors.New("ERR FT.CREATE currently supports only ON JSON")
	}
	pos++

	if pos < len(args) && strings.EqualFold(string(args[pos]), "PREFIX") {
		pos++
		if pos >= len(args) {
			return nil, errors.New("ERR syntax error")
		}

		count, err := strconv.Atoi(string(args[pos]))
		if err != nil || count < 0 {
			return nil, errors.New("ERR invalid PREFIX count")
		}
		pos++

		if len(args)-pos < count {
			return nil, errors.New("ERR syntax error")
		}

		def.Prefixes = make([]string, count)
		for i := 0; i < count; i++ {
			def.Prefixes[i] = string(args[pos+i])
		}
		pos += count
	}

	if pos >= len(args) || !strings.EqualFold(string(args[pos]), "SCHEMA") {
		return nil, errors.New("ERR FT.CREATE requires SCHEMA")
	}
	pos++

	if pos >= len(args) {
		return nil, errors.New("ERR FT.CREATE requires at least one schema field")
	}

	for pos < len(args) {
		if len(args)-pos < 4 {
			return nil, errors.New("ERR syntax error")
		}

		path := string(args[pos])
		pos++

		if !strings.EqualFold(string(args[pos]), "AS") {
			return nil, errors.New("ERR FT.CREATE requires AS for indexed JSON fields")
		}
		pos++

		alias := string(args[pos])
		pos++

		var kind engine.SearchFieldKind
		switch strings.ToUpper(string(args[pos])) {
		case "TAG":
			kind = engine.SearchFieldTag
		case "NUMERIC":
			kind = engine.SearchFieldNumeric
		default:
			return nil, errors.New("ERR unsupported search field type")
		}
		pos++

		def.Fields = append(def.Fields, engine.SearchField{
			Path:  path,
			Alias: alias,
			Kind:  kind,
		})
	}

	if err := store.CreateSearchIndex(def); err != nil {
		return nil, err
	}

	return []byte("+OK\r\n"), nil
}

func executeFTDropIndex(store *engine.Store, args [][]byte) ([]byte, error) {
	if len(args) != 2 {
		return nil, errors.New("ERR wrong number of arguments for 'ft.dropindex' command")
	}

	if !store.DropSearchIndex(string(args[1])) {
		return nil, errors.New("Unknown Index name")
	}

	return []byte("+OK\r\n"), nil
}

func executeFTList(store *engine.Store, args [][]byte) ([]byte, error) {
	if len(args) != 1 {
		return nil, errors.New("ERR wrong number of arguments for 'ft._list' command")
	}

	names := store.SearchIndexNames()
	items := make([][]byte, 0, len(names))
	for _, name := range names {
		items = append(items, formatBulkString([]byte(name)))
	}
	return array(items...), nil
}

func (s *Server) executeSearchCommand(args [][]byte) ([]byte, error) {
	switch strings.ToUpper(string(args[0])) {
	case "FT.CREATE":
		return executeFTCreate(s.store, args)
	case "FT.DROPINDEX":
		return executeFTDropIndex(s.store, args)
	case "FT._LIST":
		return executeFTList(s.store, args)
	default:
		return nil, errors.New("ERR command unavailable")
	}
}
