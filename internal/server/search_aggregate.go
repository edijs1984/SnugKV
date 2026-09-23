package server

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"snugkv/internal/engine"
)

type aggregateValue struct {
	text   string
	number float64
	isNum  bool
}

type aggregateRow struct {
	key    string
	values map[string]aggregateValue
	order  []string
}

type aggregateLoad struct {
	name string
}

type aggregateReducer struct {
	name   string
	field  string
	alias  string
}

type aggregateSort struct {
	field string
	desc  bool
}

type aggregateStageKind uint8

const (
	aggregateLoadStage aggregateStageKind = iota
	aggregateFilterStage
	aggregateGroupStage
	aggregateSortStage
	aggregateLimitStage
)

type aggregateStage struct {
	kind aggregateStageKind

	loads   []aggregateLoad
	filter  string
	groupBy []string
	reducers []aggregateReducer
	sorts   []aggregateSort
	offset  int
	count   int
}

type aggregateOptions struct {
	stages  []aggregateStage
	dialect int
}

func aggregateSchemaField(def engine.SearchDefinition, name string) (engine.SearchField, bool) {
	name = strings.TrimPrefix(name, "@")
	for _, field := range def.Fields {
		if field.Alias == name {
			return field, true
		}
	}
	return engine.SearchField{}, false
}

func aggregateProjection(store *engine.Store, def engine.SearchDefinition, key, name string) (aggregateValue, bool, error) {
	path := name
	var kind engine.SearchFieldKind
	var hasKind bool

	if strings.HasPrefix(name, "@") {
		field, ok := aggregateSchemaField(def, name)
		if !ok {
			return aggregateValue{}, false, nil
		}
		path = field.Path
		kind = field.Kind
		hasKind = true
	}

	raw, found, err := store.JSONProjection(key, path)
	if err != nil || !found {
		return aggregateValue{}, found, err
	}

	text := string(raw)
	if len(text) >= 2 && text[0] == '"' && text[len(text)-1] == '"' {
		if unquoted, err := strconv.Unquote(text); err == nil {
			text = unquoted
		}
	}

	if hasKind && kind == engine.SearchFieldTag {
		switch text {
		case "true":
			text = "1"
		case "false":
			text = "0"
		}
	}

	number, numErr := strconv.ParseFloat(text, 64)
	if numErr == nil && !math.IsNaN(number) && !math.IsInf(number, 0) {
		return aggregateValue{text: text, number: number, isNum: true}, true, nil
	}
	return aggregateValue{text: text}, true, nil
}

func aggregateSet(row *aggregateRow, name string, value aggregateValue) {
	name = strings.TrimPrefix(name, "@")
	if row.values == nil {
		row.values = make(map[string]aggregateValue)
	}
	if _, exists := row.values[name]; !exists {
		row.order = append(row.order, name)
	}
	row.values[name] = value
}

func aggregateResolve(store *engine.Store, def engine.SearchDefinition, row *aggregateRow, name string) (aggregateValue, bool, error) {
	plain := strings.TrimPrefix(name, "@")
	if value, ok := row.values[plain]; ok {
		return value, true, nil
	}

	value, found, err := aggregateProjection(store, def, row.key, name)
	if err != nil {
		return aggregateValue{}, false, err
	}
	if !found {
		return aggregateValue{}, false, nil
	}
	aggregateSet(row, plain, value)
	return value, true, nil
}

func parseAggregateOptions(args [][]byte) (aggregateOptions, error) {
	options := aggregateOptions{dialect: 1}

	for pos := 3; pos < len(args); {
		token := strings.ToUpper(string(args[pos]))
		switch token {
		case "LOAD":
			if pos+1 >= len(args) {
				return aggregateOptions{}, errors.New("SEARCH_PARSE_ARGS Bad arguments for LOAD: Expected an argument, but none provided")
			}
			count, err := strconv.Atoi(string(args[pos+1]))
			if err != nil || count < 0 {
				return aggregateOptions{}, errors.New("SEARCH_PARSE_ARGS Bad arguments for LOAD: Expected number of fields or `*`")
			}
			pos += 2
			if len(args)-pos < count {
				return aggregateOptions{}, errors.New("SEARCH_PARSE_ARGS Bad arguments for LOAD: Expected an argument, but none provided")
			}
			loads := make([]aggregateLoad, 0, count)
			for i := 0; i < count; i++ {
				if strings.EqualFold(string(args[pos]), "AS") {
					return aggregateOptions{}, fmt.Errorf("SEARCH_ARG_UNRECOGNIZED Unknown argument `AS` at position %d for <main>", pos+1)
				}
				loads = append(loads, aggregateLoad{name: string(args[pos])})
				pos++
			}
			options.stages = append(options.stages, aggregateStage{kind: aggregateLoadStage, loads: loads})

		case "FILTER":
			if pos+1 >= len(args) {
				return aggregateOptions{}, errors.New("SEARCH_PARSE_ARGS Bad arguments for APPLY/FILTER: Expected an argument, but none provided")
			}
			options.stages = append(options.stages, aggregateStage{kind: aggregateFilterStage, filter: string(args[pos+1])})
			pos += 2

		case "GROUPBY":
			if pos+1 >= len(args) {
				return aggregateOptions{}, errors.New("SEARCH_PARSE_ARGS Bad arguments for GROUPBY: Expected an argument, but none provided")
			}
			count, err := strconv.Atoi(string(args[pos+1]))
			if err != nil || count < 0 {
				return aggregateOptions{}, errors.New("SEARCH_PARSE_ARGS Bad arguments for GROUPBY: Could not convert argument to expected type")
			}
			pos += 2
			if len(args)-pos < count {
				return aggregateOptions{}, errors.New("SEARCH_PARSE_ARGS Bad arguments for GROUPBY: Expected an argument, but none provided")
			}
			groupBy := make([]string, count)
			for i := 0; i < count; i++ {
				groupBy[i] = string(args[pos])
				pos++
			}
			reducers := make([]aggregateReducer, 0, 2)
			for pos < len(args) && strings.EqualFold(string(args[pos]), "REDUCE") {
				if pos+1 >= len(args) {
					return aggregateOptions{}, errors.New("SEARCH_PARSE_ARGS Bad arguments for REDUCE: SUCCESS")
				}
				name := strings.ToUpper(string(args[pos+1]))
				pos += 2
				if pos >= len(args) {
					return aggregateOptions{}, fmt.Errorf("SEARCH_PARSE_ARGS Bad arguments for %s: Expected an argument, but none provided", name)
				}
				argc, err := strconv.Atoi(string(args[pos]))
				if err != nil || argc < 0 {
					return aggregateOptions{}, fmt.Errorf("SEARCH_PARSE_ARGS Bad arguments for %s: Could not convert argument to expected type", name)
				}
				pos++
				if len(args)-pos < argc {
					return aggregateOptions{}, fmt.Errorf("SEARCH_PARSE_ARGS Bad arguments for %s: Expected an argument, but none provided", name)
				}
				field := ""
				if argc > 0 {
					field = string(args[pos])
				}
				pos += argc

				if name == "COUNT" && argc != 0 {
					return aggregateOptions{}, errors.New("SEARCH_PARSE_ARGS Bad arguments for COUNT: Expected an argument, but none provided")
				}
				switch name {
				case "COUNT", "SUM", "MIN", "MAX", "AVG":
				default:
					return aggregateOptions{}, fmt.Errorf("SEARCH_PARSE_ARGS Unknown reducer %s", name)
				}
				if name != "COUNT" && argc != 1 {
					return aggregateOptions{}, fmt.Errorf("SEARCH_PARSE_ARGS Bad arguments for %s: Expected an argument, but none provided", name)
				}

				alias := strings.ToLower(name)
				if pos < len(args) && strings.EqualFold(string(args[pos]), "AS") {
					if pos+1 >= len(args) {
						return aggregateOptions{}, fmt.Errorf("SEARCH_PARSE_ARGS Bad arguments for %s: Expected an argument, but none provided", name)
					}
					alias = string(args[pos+1])
					pos += 2
				}
				reducers = append(reducers, aggregateReducer{name: name, field: field, alias: alias})
			}
			options.stages = append(options.stages, aggregateStage{kind: aggregateGroupStage, groupBy: groupBy, reducers: reducers})

		case "SORTBY":
			if pos+1 >= len(args) {
				return aggregateOptions{}, errors.New("SEARCH_PARSE_ARGS Bad arguments for SORTBY: Expected an argument, but none provided")
			}
			count, err := strconv.Atoi(string(args[pos+1]))
			if err != nil || count < 0 {
				return aggregateOptions{}, errors.New("SEARCH_PARSE_ARGS Bad arguments for SORTBY: Could not convert argument to expected type")
			}
			pos += 2
			if len(args)-pos < count {
				return aggregateOptions{}, errors.New("SEARCH_PARSE_ARGS Bad arguments for SORTBY: Expected an argument, but none provided")
			}
			sorts := make([]aggregateSort, 0, (count+1)/2)
			consumed := 0
			for consumed < count {
				field := string(args[pos])
				pos++
				consumed++
				desc := false
				if consumed < count {
					direction := strings.ToUpper(string(args[pos]))
					switch direction {
					case "ASC":
						pos++
						consumed++
					case "DESC":
						desc = true
						pos++
						consumed++
					default:
						return aggregateOptions{}, fmt.Errorf("SEARCH_PARSE_ARGS MISSING ASC or DESC after sort field (%s)", string(args[pos]))
					}
				}
				sorts = append(sorts, aggregateSort{field: field, desc: desc})
			}
			options.stages = append(options.stages, aggregateStage{kind: aggregateSortStage, sorts: sorts})

		case "LIMIT":
			if pos+2 >= len(args) {
				return aggregateOptions{}, errors.New("SEARCH_PARSE_ARGS LIMIT requires two arguments")
			}
			offset, err1 := strconv.Atoi(string(args[pos+1]))
			count, err2 := strconv.Atoi(string(args[pos+2]))
			if err1 != nil || err2 != nil || offset < 0 || count < 0 {
				return aggregateOptions{}, errors.New("SEARCH_PARSE_ARGS LIMIT needs two numeric arguments")
			}
			options.stages = append(options.stages, aggregateStage{kind: aggregateLimitStage, offset: offset, count: count})
			pos += 3

		case "DIALECT":
			if pos+1 >= len(args) {
				return aggregateOptions{}, errors.New("ERR syntax error")
			}
			dialect, err := strconv.Atoi(string(args[pos+1]))
			if err != nil || (dialect != 1 && dialect != 2) {
				return aggregateOptions{}, errors.New("ERR DIALECT must be 1 or 2")
			}
			options.dialect = dialect
			pos += 2

		default:
			return aggregateOptions{}, fmt.Errorf("SEARCH_ARG_UNRECOGNIZED Unknown argument `%s`", string(args[pos]))
		}
	}
	return options, nil
}

func aggregateFilterParts(expr string) ([]string, string) {
	if strings.Contains(expr, "&&") {
		return strings.Split(expr, "&&"), "&&"
	}
	return []string{expr}, ""
}

func aggregateCompare(left aggregateValue, op, right string) bool {
	right = strings.TrimSpace(right)
	right = strings.Trim(right, "\"" )

	if left.isNum {
		n, err := strconv.ParseFloat(right, 64)
		if err != nil {
			return false
		}
		switch op {
		case ">":
			return left.number > n
		case ">=":
			return left.number >= n
		case "<":
			return left.number < n
		case "<=":
			return left.number <= n
		case "==":
			return left.number == n
		case "!=":
			return left.number != n
		}
	}

	switch op {
	case "==":
		return left.text == right
	case "!=":
		return left.text != right
	case ">":
		return left.text > right
	case ">=":
		return left.text >= right
	case "<":
		return left.text < right
	case "<=":
		return left.text <= right
	}
	return false
}

func evaluateAggregateFilter(store *engine.Store, def engine.SearchDefinition, row *aggregateRow, expr string) (bool, error) {
	parts, _ := aggregateFilterParts(expr)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		fields := strings.Fields(part)
		if len(fields) != 3 || !strings.HasPrefix(fields[0], "@") {
			if len(parts) == 1 {
				return false, fmt.Errorf("SEARCH_EXPR Unknown symbol '%s'", strings.TrimSpace(expr))
			}
			return false, errors.New("SEARCH_EXPR Invalid expression")
		}
		value, found, err := aggregateResolve(store, def, row, fields[0])
		if err != nil {
			return false, err
		}
		if !found {
			return false, fmt.Errorf("SEARCH_PROP_NOT_FOUND Property not loaded nor in pipeline: `%s`", strings.TrimPrefix(fields[0], "@"))
		}
		if !aggregateCompare(value, fields[1], fields[2]) {
			return false, nil
		}
	}
	return true, nil
}

func executeFTAggregate(store *engine.Store, args [][]byte) ([]byte, error) {
	if len(args) < 3 {
		return nil, errors.New("ERR wrong number of arguments for 'FT.AGGREGATE' command")
	}

	indexName := string(args[1])
	def, ok := store.SearchDefinition(indexName)
	if !ok {
		return nil, errors.New("SEARCH_INDEX_NOT_FOUND Index not found: " + indexName)
	}

	options, err := parseAggregateOptions(args)
	if err != nil {
		return nil, err
	}
	queryNode, err := parseSearchQuery(string(args[2]), options.dialect)
	if err != nil {
		return nil, err
	}
	if err := validateSearchTextAliases(def, queryNode); err != nil {
		return nil, err
	}
	if err := validateSearchPhraseStopwords(def, queryNode, string(args[2])); err != nil {
		return nil, err
	}
	if queryNode != nil {
		var keep bool
		queryNode, keep = stripSearchStopwords(def, queryNode)
		if !keep {
			return array(integer(0)), nil
		}
	}

	allKeys, ok := store.SearchAllKeys(indexName)
	if !ok {
		return nil, errors.New("SEARCH_INDEX_NOT_FOUND Index not found: " + indexName)
	}
	candidates := allKeys
	if queryNode != nil {
		candidates, err = evaluateSearchQuery(store, indexName, def, queryNode, allKeys, def.Language)
		if err != nil {
			return nil, err
		}
	}

	rows := make([]aggregateRow, 0, len(candidates))
	for _, key := range candidates {
		rows = append(rows, aggregateRow{key: key, values: make(map[string]aggregateValue)})
	}

	if len(options.stages) == 0 {
		if len(rows) == 0 {
			return array(integer(0)), nil
		}
		return array(integer(1), array()), nil
	}

	reportedTotal := -1
	filterOnlyImplicitRows := false
	for stageIndex, stage := range options.stages {
		switch stage.kind {
		case aggregateLoadStage:
			for i := range rows {
				for _, load := range stage.loads {
					value, found, err := aggregateProjection(store, def, rows[i].key, load.name)
					if err != nil {
						return nil, err
					}
					if !found {
						continue
					}
					name := strings.TrimPrefix(load.name, "@")
					aggregateSet(&rows[i], name, value)
				}
			}

		case aggregateFilterStage:
			// Redis FT.AGGREGATE reports the cardinality entering FILTER when
			// earlier pipeline stages already materialized rows (e.g. LOAD).
			if reportedTotal < 0 && stageIndex > 0 {
				reportedTotal = len(rows)
			}
			// With FILTER as the first/only materializing stage Redis reports
			// one aggregate row while exposing the referenced property values.
			if stageIndex == 0 {
				filterOnlyImplicitRows = true
			}
			filtered := make([]aggregateRow, 0, len(rows))
			for i := range rows {
				match, err := evaluateAggregateFilter(store, def, &rows[i], stage.filter)
				if err != nil {
					return nil, err
				}
				if match {
					filtered = append(filtered, rows[i])
				}
			}
			rows = filtered

		case aggregateGroupStage:
			type group struct {
				row   aggregateRow
				count int
				sums  map[string]float64
				mins  map[string]float64
				maxs  map[string]float64
				nums  map[string]int
			}
			groups := make(map[string]*group)
			order := make([]string, 0)
			for i := range rows {
				parts := make([]string, 0, len(stage.groupBy))
				base := aggregateRow{values: make(map[string]aggregateValue)}
				for _, field := range stage.groupBy {
					value, found, err := aggregateResolve(store, def, &rows[i], field)
					if err != nil {
						return nil, err
					}
					if !found {
						value = aggregateValue{}
					}
					parts = append(parts, value.text)
					aggregateSet(&base, strings.TrimPrefix(field, "@"), value)
				}
				id := strings.Join(parts, "\x00")
				g := groups[id]
				if g == nil {
					g = &group{row: base, sums: map[string]float64{}, mins: map[string]float64{}, maxs: map[string]float64{}, nums: map[string]int{}}
					groups[id] = g
					order = append(order, id)
				}
				g.count++
				for _, reducer := range stage.reducers {
					if reducer.name == "COUNT" {
						continue
					}
					value, found, err := aggregateResolve(store, def, &rows[i], reducer.field)
					if err != nil {
						return nil, err
					}
					if !found || !value.isNum {
						continue
					}
					g.sums[reducer.alias] += value.number
					if g.nums[reducer.alias] == 0 || value.number < g.mins[reducer.alias] {
						g.mins[reducer.alias] = value.number
					}
					if g.nums[reducer.alias] == 0 || value.number > g.maxs[reducer.alias] {
						g.maxs[reducer.alias] = value.number
					}
					g.nums[reducer.alias]++
				}
			}

			next := make([]aggregateRow, 0, len(order))
			for _, id := range order {
				g := groups[id]
				for _, reducer := range stage.reducers {
					var number float64
					switch reducer.name {
					case "COUNT":
						number = float64(g.count)
					case "SUM":
						number = g.sums[reducer.alias]
					case "MIN":
						number = g.mins[reducer.alias]
					case "MAX":
						number = g.maxs[reducer.alias]
					case "AVG":
						if g.nums[reducer.alias] > 0 {
							number = g.sums[reducer.alias] / float64(g.nums[reducer.alias])
						}
					}
					text := strconv.FormatFloat(number, 'g', -1, 64)
					aggregateSet(&g.row, reducer.alias, aggregateValue{text: text, number: number, isNum: true})
				}
				next = append(next, g.row)
			}
			rows = next

		case aggregateSortStage:
			for i := range rows {
				for _, spec := range stage.sorts {
					if _, found, err := aggregateResolve(store, def, &rows[i], spec.field); err != nil {
						return nil, err
					} else if !found {
						return nil, fmt.Errorf("SEARCH_PROP_NOT_FOUND Property not loaded nor in pipeline: `%s`", strings.TrimPrefix(spec.field, "@"))
					}
				}
			}
			sort.SliceStable(rows, func(i, j int) bool {
				for _, spec := range stage.sorts {
					left := rows[i].values[strings.TrimPrefix(spec.field, "@")]
					right := rows[j].values[strings.TrimPrefix(spec.field, "@")]
					var less, equal bool
					if left.isNum && right.isNum {
						less = left.number < right.number
						equal = left.number == right.number
					} else {
						less = left.text < right.text
						equal = left.text == right.text
					}
					if equal {
						continue
					}
					if spec.desc {
						return !less
					}
					return less
				}
				return rows[i].key < rows[j].key
			})

		case aggregateLimitStage:
			if reportedTotal < 0 {
				// Measured Redis behavior reports the remaining logical
				// cardinality after applying the offset, not always the full
				// pre-LIMIT row count.
				reportedTotal = len(rows) - stage.offset
				if reportedTotal < 0 {
					reportedTotal = 0
				}
			}
			start := stage.offset
			if start > len(rows) {
				start = len(rows)
			}
			end := start + stage.count
			if end > len(rows) {
				end = len(rows)
			}
			rows = rows[start:end]
		}
	}

	if reportedTotal < 0 {
		if filterOnlyImplicitRows && len(rows) > 0 {
			reportedTotal = 1
		} else {
			reportedTotal = len(rows)
		}
	}

	if filterOnlyImplicitRows && len(rows) > 1 {
		merged := aggregateRow{values: make(map[string]aggregateValue)}
		for _, row := range rows {
			for _, name := range row.order {
				// Preserve repeated FILTER-materialized property names in the
				// wire row by giving each occurrence a synthetic order token.
				value := row.values[name]
				synthetic := name
				for {
					if _, exists := merged.values[synthetic]; !exists {
						break
					}
					synthetic += "\x00"
				}
				merged.values[synthetic] = value
				merged.order = append(merged.order, synthetic)
			}
		}
		rows = []aggregateRow{merged}
	}

	items := make([][]byte, 0, len(rows)+1)
	items = append(items, integer(int64(reportedTotal)))
	for _, row := range rows {
		fields := make([][]byte, 0, len(row.order)*2)
		for _, name := range row.order {
			value := row.values[name]
			wireName := strings.TrimRight(name, "\x00")
			fields = append(fields,
				formatBulkString([]byte(wireName)),
				formatBulkString([]byte(value.text)),
			)
		}
		items = append(items, array(fields...))
	}
	return array(items...), nil
}
