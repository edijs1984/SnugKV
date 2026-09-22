package server

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"snugkv/internal/engine"
)

var searchCommands = map[string]commandInfo{
	"FT.CREATE":    {6, 0, 0, 0, 0, true},
	"FT.DROPINDEX": {2, 2, 0, 0, 0, true},
	"FT._LIST":     {1, 1, 0, 0, 0, false},
	"FT.INFO":      {2, 2, 0, 0, 0, false},
	"FT.SEARCH":    {3, 0, 0, 0, 0, false},
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
		return nil, errors.New("SEARCH_INDEX_NOT_FOUND Index not found: " + string(args[1]))
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


func executeFTInfo(store *engine.Store, args [][]byte) ([]byte, error) {
	if len(args) != 2 {
		return nil, errors.New("ERR wrong number of arguments for 'ft.info' command")
	}

	name := string(args[1])
	def, ok := store.SearchDefinition(name)
	if !ok {
		return nil, errors.New("SEARCH_INDEX_NOT_FOUND Index not found: " + name)
	}

	keys, ok := store.SearchAllKeys(name)
	if !ok {
		return nil, errors.New("SEARCH_INDEX_NOT_FOUND Index not found: " + name)
	}

	prefixItems := make([][]byte, 0, len(def.Prefixes))
	for _, prefix := range def.Prefixes {
		prefixItems = append(prefixItems, formatBulkString([]byte(prefix)))
	}

	attributes := make([][]byte, 0, len(def.Fields))
	for _, field := range def.Fields {
		fieldType := "TAG"
		if field.Kind == engine.SearchFieldNumeric {
			fieldType = "NUMERIC"
		}
		attributes = append(attributes, array(
			formatBulkString([]byte("identifier")),
			formatBulkString([]byte(field.Path)),
			formatBulkString([]byte("attribute")),
			formatBulkString([]byte(field.Alias)),
			formatBulkString([]byte("type")),
			formatBulkString([]byte(fieldType)),
		))
	}

	return array(
		formatBulkString([]byte("index_name")),
		formatBulkString([]byte(def.Name)),
		formatBulkString([]byte("index_options")),
		array(),
		formatBulkString([]byte("index_definition")),
		array(
			formatBulkString([]byte("key_type")),
			formatBulkString([]byte("JSON")),
			formatBulkString([]byte("prefixes")),
			array(prefixItems...),
			formatBulkString([]byte("default_score")),
			formatBulkString([]byte("1")),
		),
		formatBulkString([]byte("attributes")),
		array(attributes...),
		formatBulkString([]byte("num_docs")),
		integer(int64(len(keys))),
		formatBulkString([]byte("indexing")),
		integer(0),
		formatBulkString([]byte("percent_indexed")),
		formatBulkString([]byte("1")),
	), nil
}



type searchQueryClause struct {
	alias   string
	tag     *string
	minimum *float64
	maximum *float64
}

type searchReturnField struct {
	path  string
	alias string
}

type searchOptions struct {
	offset       int
	count        int
	noContent    bool
	returnFields []searchReturnField
}


func splitSearchTerms(query string) ([]string, error) {
	var terms []string
	start := -1
	depth := 0

	for i, r := range query {
		switch r {
		case '{', '[':
			if start < 0 {
				start = i
			}
			depth++
		case '}', ']':
			if depth == 0 {
				return nil, errors.New("ERR unsupported search query")
			}
			depth--
		case ' ', '\t', '\n', '\r':
			if depth == 0 {
				if start >= 0 {
					terms = append(terms, strings.TrimSpace(query[start:i]))
					start = -1
				}
				continue
			}
		default:
			if start < 0 {
				start = i
			}
		}
	}

	if depth != 0 {
		return nil, errors.New("ERR unsupported search query")
	}
	if start >= 0 {
		terms = append(terms, strings.TrimSpace(query[start:]))
	}
	return terms, nil
}

func parseSearchBound(value string) (float64, error) {
	switch strings.ToLower(value) {
	case "-inf":
		return -math.MaxFloat64, nil
	case "+inf", "inf":
		return math.MaxFloat64, nil
	default:
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return 0, errors.New("invalid numeric bound")
		}
		return parsed, nil
	}
}

func parseSearchQuery(query string) ([]searchQueryClause, error) {
	query = strings.TrimSpace(query)
	if query == "*" {
		return nil, nil
	}
	if query == "" {
		return nil, errors.New("ERR invalid search query")
	}

	parts, err := splitSearchTerms(query)
	if err != nil {
		return nil, err
	}
	clauses := make([]searchQueryClause, 0, len(parts))

	for _, part := range parts {
		if !strings.HasPrefix(part, "@") {
			return nil, errors.New("ERR unsupported search query")
		}

		colon := strings.IndexByte(part, ':')
		if colon <= 1 || colon == len(part)-1 {
			return nil, errors.New("ERR unsupported search query")
		}

		alias := part[1:colon]
		expr := part[colon+1:]

		if strings.HasPrefix(expr, "{") && strings.HasSuffix(expr, "}") {
			value := expr[1 : len(expr)-1]
			if value == "" {
				return nil, errors.New("ERR unsupported search query")
			}
			clauses = append(clauses, searchQueryClause{
				alias: alias,
				tag:   &value,
			})
			continue
		}

		if strings.HasPrefix(expr, "[") && strings.HasSuffix(expr, "]") {
			rangeBody := strings.TrimSpace(expr[1 : len(expr)-1])
			bounds := strings.Fields(rangeBody)
			if len(bounds) != 2 {
				return nil, errors.New("ERR unsupported numeric range")
			}

			minimum, err := parseSearchBound(bounds[0])
			if err != nil {
				offset := strings.Index(query, bounds[0])
				return nil, fmt.Errorf("SEARCH_SYNTAX Syntax error at offset %d near %s", offset, bounds[0])
			}
			maximum, err := parseSearchBound(bounds[1])
			if err != nil {
				offset := strings.Index(query, bounds[1])
				return nil, fmt.Errorf("SEARCH_SYNTAX Syntax error at offset %d near %s", offset, bounds[1])
			}

			clauses = append(clauses, searchQueryClause{
				alias:   alias,
				minimum: &minimum,
				maximum: &maximum,
			})
			continue
		}

		return nil, errors.New("ERR unsupported search query")
	}

	return clauses, nil
}

func parseSearchOptions(args [][]byte) (searchOptions, error) {
	options := searchOptions{
		offset: 0,
		count:  10,
	}

	for pos := 3; pos < len(args); {
		switch strings.ToUpper(string(args[pos])) {
		case "NOCONTENT":
			options.noContent = true
			pos++

		case "LIMIT":
			if pos+2 >= len(args) {
				return searchOptions{}, errors.New("ERR syntax error")
			}
			offset, err := strconv.Atoi(string(args[pos+1]))
			if err != nil || offset < 0 {
				return searchOptions{}, errors.New("ERR invalid LIMIT")
			}
			count, err := strconv.Atoi(string(args[pos+2]))
			if err != nil || count < 0 {
				return searchOptions{}, errors.New("ERR invalid LIMIT")
			}
			options.offset = offset
			options.count = count
			pos += 3

		case "RETURN":
			if pos+1 >= len(args) {
				return searchOptions{}, errors.New("ERR syntax error")
			}
			tokenCount, err := strconv.Atoi(string(args[pos+1]))
			if err != nil || tokenCount < 0 {
				return searchOptions{}, errors.New("ERR invalid RETURN count")
			}
			pos += 2
			if len(args)-pos < tokenCount {
				return searchOptions{}, errors.New("ERR syntax error")
			}
			if tokenCount == 0 {
				options.noContent = true
				continue
			}

			end := pos + tokenCount
			for pos < end {
				path := string(args[pos])
				if !strings.HasPrefix(path, "$") {
					return searchOptions{}, errors.New("ERR RETURN currently supports JSONPath identifiers only")
				}
				pos++

				alias := path
				if pos < end && strings.EqualFold(string(args[pos]), "AS") {
					if pos+1 >= end {
						return searchOptions{}, errors.New("ERR syntax error")
					}
					alias = string(args[pos+1])
					pos += 2
				}

				options.returnFields = append(options.returnFields, searchReturnField{
					path:  path,
					alias: alias,
				})
			}

		default:
			return searchOptions{}, errors.New("ERR unsupported FT.SEARCH option")
		}
	}

	return options, nil
}

func intersectSortedSearchKeys(sets ...[]string) []string {
	if len(sets) == 0 {
		return nil
	}

	counts := make(map[string]int, len(sets[0]))
	for _, key := range sets[0] {
		counts[key] = 1
	}

	for i := 1; i < len(sets); i++ {
		seen := make(map[string]struct{}, len(sets[i]))
		for _, key := range sets[i] {
			seen[key] = struct{}{}
		}
		for key, count := range counts {
			if count != i {
				continue
			}
			if _, ok := seen[key]; ok {
				counts[key] = i + 1
			}
		}
	}

	result := make([]string, 0)
	for _, key := range sets[0] {
		if counts[key] == len(sets) {
			result = append(result, key)
		}
	}
	return result
}

func executeFTSearch(store *engine.Store, args [][]byte) ([]byte, error) {
	if len(args) < 3 {
		return nil, errors.New("ERR wrong number of arguments for 'ft.search' command")
	}

	indexName := string(args[1])
	clauses, err := parseSearchQuery(string(args[2]))
	if err != nil {
		return nil, err
	}
	options, err := parseSearchOptions(args)
	if err != nil {
		return nil, err
	}

	allKeys, ok := store.SearchAllKeys(indexName)
	if !ok {
		return nil, errors.New("SEARCH_INDEX_NOT_FOUND Index not found: " + indexName)
	}

	candidates := allKeys
	if len(clauses) > 0 {
		sets := make([][]string, 0, len(clauses))
		for _, clause := range clauses {
			switch {
			case clause.tag != nil:
				keys, ok := store.SearchTagKeys(indexName, clause.alias, *clause.tag)
				if !ok {
					return nil, errors.New("SEARCH_INDEX_NOT_FOUND Index not found: " + indexName)
				}
				sets = append(sets, keys)

			case clause.minimum != nil && clause.maximum != nil:
				keys, ok := store.SearchNumericRangeKeys(
					indexName,
					clause.alias,
					*clause.minimum,
					*clause.maximum,
				)
				if !ok {
					return nil, errors.New("SEARCH_INDEX_NOT_FOUND Index not found: " + indexName)
				}
				sets = append(sets, keys)

			default:
				return nil, errors.New("ERR unsupported search query")
			}
		}
		candidates = intersectSortedSearchKeys(sets...)
	}

	type hit struct {
		key string
		raw []byte
	}

	hits := make([]hit, 0, len(candidates))
	for _, key := range candidates {
		raw, found, err := store.JSONGet(key, ".")
		if err != nil {
			continue
		}
		if !found {
			continue
		}
		hits = append(hits, hit{key: key, raw: raw})
	}

	total := len(hits)

	start := options.offset
	if start > total {
		start = total
	}
	end := start + options.count
	if end > total {
		end = total
	}

	items := make([][]byte, 0, 1+(end-start)*2)
	items = append(items, integer(int64(total)))

	for _, hit := range hits[start:end] {
		items = append(items, formatBulkString([]byte(hit.key)))
		if options.noContent {
			continue
		}

		if len(options.returnFields) == 0 {
			items = append(items, array(
				formatBulkString([]byte("$")),
				formatBulkString(hit.raw),
			))
			continue
		}

		fields := make([][]byte, 0, len(options.returnFields)*2)
		for _, projection := range options.returnFields {
			value, found, err := store.JSONProjection(hit.key, projection.path)
			if err != nil {
				return nil, err
			}
			if !found {
				continue
			}

			fields = append(
				fields,
				formatBulkString([]byte(projection.alias)),
				formatBulkString(value),
			)
		}
		items = append(items, array(fields...))
	}

	return array(items...), nil
}

func (s *Server) executeSearchCommand(args [][]byte) ([]byte, error) {
	switch strings.ToUpper(string(args[0])) {
	case "FT.CREATE":
		return s.executeSearchDefinitionMutation(args, func() ([]byte, error) {
			return executeFTCreate(s.store, args)
		})
	case "FT.DROPINDEX":
		return s.executeSearchDefinitionMutation(args, func() ([]byte, error) {
			return executeFTDropIndex(s.store, args)
		})
	case "FT._LIST":
		return executeFTList(s.store, args)
	case "FT.INFO":
		return executeFTInfo(s.store, args)
	case "FT.SEARCH":
		return executeFTSearch(s.store, args)
	default:
		return nil, errors.New("ERR command unavailable")
	}
}
