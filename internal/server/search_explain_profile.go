package server

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"snugkv/internal/engine"
)

func searchExplainClause(clause *searchQueryClause) string {
	if clause == nil {
		return "<nil>"
	}
	if clause.noMatch {
		return "EMPTY"
	}

	prefix := ""
	if clause.alias != "" {
		prefix = "@" + clause.alias + ":"
	}

	switch {
	case clause.tag != nil:
		return prefix + "TAG{" + *clause.tag + "}"
	case clause.textPhrase != nil:
		return prefix + "PHRASE{" + *clause.textPhrase + "}"
	case clause.text != nil:
		value := *clause.text
		switch {
		case clause.textFuzzy > 0:
			return prefix + fmt.Sprintf("FUZZY(%d){%s}", clause.textFuzzy, value)
		case clause.textWildcardLeading && clause.textWildcardTrailing:
			return prefix + "WILDCARD{*" + value + "*}"
		case clause.textWildcardLeading:
			return prefix + "WILDCARD{*" + value + "}"
		case clause.textWildcardTrailing || clause.textPrefix:
			return prefix + "PREFIX{" + value + "*}"
		default:
			return prefix + "TEXT{" + value + "}"
		}
	case clause.minimum != nil || clause.maximum != nil:
		min := "-inf"
		max := "+inf"
		if clause.minimum != nil {
			min = strconv.FormatFloat(*clause.minimum, 'g', -1, 64)
		}
		if clause.maximum != nil {
			max = strconv.FormatFloat(*clause.maximum, 'g', -1, 64)
		}
		return prefix + "NUMERIC{" + min + "," + max + "}"
	case clause.geo != nil:
		return prefix + fmt.Sprintf(
			"GEO{%g,%g,%gm}",
			clause.geo.longitude,
			clause.geo.latitude,
			clause.geo.radiusMeters,
		)
	default:
		return prefix + "CLAUSE"
	}
}

func appendSearchExplainLines(lines *[]string, node *searchQueryNode, depth int) {
	indent := strings.Repeat("  ", depth)
	if node == nil {
		*lines = append(*lines, indent+"WILDCARD")
		return
	}

	switch node.kind {
	case searchQueryClauseNode:
		*lines = append(*lines, indent+searchExplainClause(node.clause))

	case searchQueryAndNode:
		*lines = append(*lines, indent+"INTERSECT {")
		appendSearchExplainLines(lines, node.left, depth+1)
		appendSearchExplainLines(lines, node.right, depth+1)
		*lines = append(*lines, indent+"}")

	case searchQueryOrNode:
		*lines = append(*lines, indent+"UNION {")
		appendSearchExplainLines(lines, node.left, depth+1)
		appendSearchExplainLines(lines, node.right, depth+1)
		*lines = append(*lines, indent+"}")

	case searchQueryNotNode:
		*lines = append(*lines, indent+"NOT {")
		appendSearchExplainLines(lines, node.child, depth+1)
		*lines = append(*lines, indent+"}")

	default:
		*lines = append(*lines, indent+"UNKNOWN")
	}
}

func parseExplainDialect(args [][]byte) (int, error) {
	dialect := 1
	if len(args) == 3 {
		return dialect, nil
	}
	if len(args) != 5 || !strings.EqualFold(string(args[3]), "DIALECT") {
		return 0, errors.New("ERR syntax error")
	}
	value, err := strconv.Atoi(string(args[4]))
	if err != nil || (value != 1 && value != 2) {
		return 0, errors.New("ERR unsupported search dialect")
	}
	return value, nil
}

func executeFTExplain(store *engine.Store, args [][]byte, cli bool) ([]byte, error) {
	if len(args) != 3 && len(args) != 5 {
		return nil, errors.New("ERR wrong number of arguments")
	}

	indexName := string(args[1])
	def, ok := store.SearchDefinition(indexName)
	if !ok {
		return nil, errors.New("SEARCH_INDEX_NOT_FOUND Index not found: " + indexName)
	}

	dialect, err := parseExplainDialect(args)
	if err != nil {
		return nil, err
	}
	node, err := parseSearchQuery(string(args[2]), dialect)
	if err != nil {
		return nil, err
	}
	if err := validateSearchTextAliases(def, node); err != nil {
		return nil, err
	}

	lines := make([]string, 0, 8)
	appendSearchExplainLines(&lines, node, 0)
	lines = append(lines, "")

	if cli {
		items := make([][]byte, 0, len(lines))
		for _, line := range lines {
			items = append(items, formatBulkString([]byte(line)))
		}
		return array(items...), nil
	}
	return formatBulkString([]byte(strings.Join(lines, "\n"))), nil
}

func profileInfoReply(kind string, limited bool, elapsed time.Duration) []byte {
	items := [][]byte{
		formatBulkString([]byte("Total profile time")),
		formatBulkString([]byte(strconv.FormatFloat(float64(elapsed.Nanoseconds())/1e6, 'f', 3, 64))),
		formatBulkString([]byte("Parsing time")),
		formatBulkString([]byte("0")),
		formatBulkString([]byte("Pipeline creation time")),
		formatBulkString([]byte("0")),
		formatBulkString([]byte("Result processors profile")),
		array(
			formatBulkString([]byte("Type")),
			formatBulkString([]byte(kind)),
		),
	}
	if !limited {
		items = append(items,
			formatBulkString([]byte("Iterators profile")),
			array(
				formatBulkString([]byte("Type")),
				formatBulkString([]byte("SNUGKV_EXACT")),
			),
		)
	}
	return array(items...)
}

func (s *Server) executeFTProfile(args [][]byte) ([]byte, error) {
	if len(args) < 5 {
		return nil, errors.New("ERR wrong number of arguments for 'ft.profile' command")
	}

	indexName := string(args[1])
	target, ok := s.store.ResolveSearchIndexName(indexName)
	if !ok {
		return nil, errors.New("SEARCH_INDEX_NOT_FOUND Index not found: " + indexName)
	}

	queryType := strings.ToUpper(string(args[2]))
	pos := 3
	limited := false
	if pos < len(args) && strings.EqualFold(string(args[pos]), "LIMITED") {
		limited = true
		pos++
	}
	if pos >= len(args) || !strings.EqualFold(string(args[pos]), "QUERY") {
		return nil, errors.New("ERR syntax error")
	}
	pos++
	if pos >= len(args) {
		return nil, errors.New("ERR syntax error")
	}

	nested := make([][]byte, 0, len(args)-pos+3)
	start := time.Now()

	var result []byte
	var err error
	switch queryType {
	case "SEARCH":
		nested = append(nested, []byte("FT.SEARCH"), []byte(target))
		nested = append(nested, args[pos:]...)
		result, err = executeFTSearch(s.store, nested)
	case "AGGREGATE":
		nested = append(nested, []byte("FT.AGGREGATE"), []byte(target))
		nested = append(nested, args[pos:]...)
		result, err = executeFTAggregate(s, nested)
	default:
		return nil, errors.New("ERR syntax error")
	}
	if err != nil {
		return nil, err
	}

	elapsed := time.Since(start)
	return array(
		result,
		profileInfoReply(queryType, limited, elapsed),
	), nil
}
