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

	for pos < len(args) && !strings.EqualFold(string(args[pos]), "SCHEMA") {
		switch strings.ToUpper(string(args[pos])) {
		case "PREFIX":
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

		case "STOPWORDS":
			def.StopwordsConfigured = true
			pos++
			if pos >= len(args) {
				return nil, errors.New("ERR syntax error")
			}
			count, err := strconv.Atoi(string(args[pos]))
			if err != nil || count < 0 {
				return nil, errors.New("ERR invalid STOPWORDS count")
			}
			pos++
			if len(args)-pos < count {
				return nil, errors.New("ERR syntax error")
			}
			def.Stopwords = make([]string, count)
			for i := 0; i < count; i++ {
				def.Stopwords[i] = string(args[pos+i])
			}
			pos += count

		case "LANGUAGE":
			if pos+1 >= len(args) {
				return nil, errors.New("ERR syntax error")
			}
			def.Language = strings.ToLower(string(args[pos+1]))
			if !engine.SearchLanguageSupported(def.Language) {
				return nil, errors.New("SEARCH_ADD_ARGS Invalid language")
			}
			pos += 2

		case "LANGUAGE_FIELD":
			if pos+1 >= len(args) {
				return nil, errors.New("ERR syntax error")
			}
			def.LanguageField = string(args[pos+1])
			pos += 2

		default:
			return nil, errors.New("ERR unsupported FT.CREATE option")
		}
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
		case "TEXT":
			kind = engine.SearchFieldText
		default:
			return nil, errors.New("ERR unsupported search field type")
		}
		pos++

		noStem := false
		if kind == engine.SearchFieldText && pos < len(args) && strings.EqualFold(string(args[pos]), "NOSTEM") {
			noStem = true
			pos++
		}

		def.Fields = append(def.Fields, engine.SearchField{
			Path:   path,
			Alias:  alias,
			Kind:   kind,
			NoStem: noStem,
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
		switch field.Kind {
		case engine.SearchFieldNumeric:
			fieldType = "NUMERIC"
		case engine.SearchFieldText:
			fieldType = "TEXT"
		}
		fieldItems := [][]byte{
			formatBulkString([]byte("identifier")),
			formatBulkString([]byte(field.Path)),
			formatBulkString([]byte("attribute")),
			formatBulkString([]byte(field.Alias)),
			formatBulkString([]byte("type")),
			formatBulkString([]byte(fieldType)),
		}
		if field.Kind == engine.SearchFieldText && field.NoStem {
			fieldItems = append(fieldItems, formatBulkString([]byte("NOSTEM")))
		}
		attributes = append(attributes, array(fieldItems...))
	}

	return array(
		formatBulkString([]byte("index_name")),
		formatBulkString([]byte(def.Name)),
		formatBulkString([]byte("index_options")),
		array(),
		formatBulkString([]byte("index_definition")),
		func() []byte {
			items := [][]byte{
				formatBulkString([]byte("key_type")),
				formatBulkString([]byte("JSON")),
				formatBulkString([]byte("prefixes")),
				array(prefixItems...),
			}
			if def.LanguageField != "" {
				items = append(items,
					formatBulkString([]byte("language_field")),
					formatBulkString([]byte(def.LanguageField)),
				)
			}
			items = append(items,
				formatBulkString([]byte("default_score")),
				formatBulkString([]byte("1")),
			)
			return array(items...)
		}(),
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
	alias      string
	tag        *string
	text       *string
	textPhrase *string
	textPrefix bool
	minimum    *float64
	maximum    *float64
}

type searchQueryNodeKind uint8

const (
	searchQueryClauseNode searchQueryNodeKind = iota
	searchQueryAndNode
	searchQueryOrNode
	searchQueryNotNode
)

type searchQueryNode struct {
	kind   searchQueryNodeKind
	clause *searchQueryClause
	left   *searchQueryNode
	right  *searchQueryNode
	child  *searchQueryNode
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
	sortBy       string
	sortDesc     bool
	dialect      int
	language     string
}


type searchQueryParser struct {
	query string
	pos   int
}

func (p *searchQueryParser) skipSpace() {
	for p.pos < len(p.query) {
		switch p.query[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func isSearchSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r':
		return true
	default:
		return false
	}
}

func (p *searchQueryParser) parse() (*searchQueryNode, error) {
	p.skipSpace()
	if p.pos >= len(p.query) {
		return nil, errors.New("ERR invalid search query")
	}

	node, err := p.parseOr()
	if err != nil {
		return nil, err
	}

	p.skipSpace()
	if p.pos != len(p.query) {
		return nil, errors.New("ERR unsupported search query")
	}
	return node, nil
}

func (p *searchQueryParser) parseOr() (*searchQueryNode, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}

	for {
		p.skipSpace()
		if p.pos >= len(p.query) || p.query[p.pos] != '|' {
			return left, nil
		}
		if p.pos == 0 || p.pos+1 >= len(p.query) ||
			!isSearchSpace(p.query[p.pos-1]) || !isSearchSpace(p.query[p.pos+1]) {
			return nil, errors.New("ERR unsupported search query")
		}
		p.pos++
		p.skipSpace()
		if p.pos >= len(p.query) {
			return nil, errors.New("ERR unsupported search query")
		}

		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &searchQueryNode{
			kind:  searchQueryOrNode,
			left:  left,
			right: right,
		}
	}
}

func (p *searchQueryParser) parseAnd() (*searchQueryNode, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}

	for {
		beforeSpace := p.pos
		p.skipSpace()

		if p.pos >= len(p.query) || p.query[p.pos] == ')' || p.query[p.pos] == '|' {
			return left, nil
		}

		// Implicit AND requires a separator between adjacent expressions.
		if beforeSpace == p.pos {
			return nil, errors.New("ERR unsupported search query")
		}

		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = &searchQueryNode{
			kind:  searchQueryAndNode,
			left:  left,
			right: right,
		}
	}
}

func (p *searchQueryParser) parseUnary() (*searchQueryNode, error) {
	p.skipSpace()
	if p.pos >= len(p.query) {
		return nil, errors.New("ERR unsupported search query")
	}

	if p.query[p.pos] == '-' {
		p.pos++
		child, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &searchQueryNode{
			kind:  searchQueryNotNode,
			child: child,
		}, nil
	}

	return p.parsePrimary()
}

func (p *searchQueryParser) parseFieldTextGroup() (*searchQueryNode, bool, error) {
	start := p.pos
	if start >= len(p.query) || p.query[start] != '@' {
		return nil, false, nil
	}

	colonRel := strings.IndexByte(p.query[start:], ':')
	if colonRel <= 1 {
		return nil, false, nil
	}
	colon := start + colonRel
	if colon+1 >= len(p.query) || p.query[colon+1] != '(' {
		return nil, false, nil
	}

	alias := p.query[start+1 : colon]
	pos := colon + 2
	groupStart := pos
	depth := 1
	closePos := -1
	for pos < len(p.query) {
		switch p.query[pos] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				closePos = pos
			}
		}
		if closePos >= 0 {
			break
		}
		pos++
	}

	if depth != 0 || closePos < 0 {
		return nil, true, errors.New("ERR unsupported search query")
	}
	pos = closePos

	body := strings.TrimSpace(p.query[groupStart:pos])
	if body == "" || strings.ContainsAny(body, "()|{}[]\"") {
		return nil, true, errors.New("ERR unsupported search query")
	}

	terms := strings.Fields(body)
	if len(terms) == 0 {
		return nil, true, errors.New("ERR unsupported search query")
	}

	var node *searchQueryNode
	for _, term := range terms {
		if strings.HasPrefix(term, "-") || term == "" {
			return nil, true, errors.New("ERR unsupported search query")
		}
		prefix := strings.HasSuffix(term, "*")
		if prefix {
			if strings.Count(term, "*") != 1 || len(term) == 1 {
				return nil, true, errors.New("ERR unsupported search query")
			}
			term = strings.TrimSuffix(term, "*")
		} else if strings.Contains(term, "*") {
			return nil, true, errors.New("ERR unsupported search query")
		}
		value := strings.ToLower(term)
		clause := &searchQueryNode{
			kind: searchQueryClauseNode,
			clause: &searchQueryClause{
				alias:      alias,
				text:       &value,
				textPrefix: prefix,
			},
		}
		if node == nil {
			node = clause
		} else {
			node = &searchQueryNode{
				kind:  searchQueryAndNode,
				left:  node,
				right: clause,
			}
		}
	}

	p.pos = pos + 1
	return node, true, nil
}

func (p *searchQueryParser) parsePrimary() (*searchQueryNode, error) {
	p.skipSpace()
	if p.pos >= len(p.query) {
		return nil, errors.New("ERR unsupported search query")
	}

	if node, matched, err := p.parseFieldTextGroup(); matched {
		return node, err
	}

	if p.query[p.pos] == '(' {
		p.pos++
		node, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if p.pos >= len(p.query) || p.query[p.pos] != ')' {
			return nil, errors.New("ERR unsupported search query")
		}
		p.pos++
		return node, nil
	}

	part, err := p.readClause()
	if err != nil {
		return nil, err
	}
	clause, err := parseSearchClause(part, p.query)
	if err != nil {
		return nil, err
	}
	return &searchQueryNode{
		kind:   searchQueryClauseNode,
		clause: &clause,
	}, nil
}

func (p *searchQueryParser) readClause() (string, error) {
	start := p.pos
	depth := 0
	quoted := false

	for p.pos < len(p.query) {
		switch p.query[p.pos] {
		case '"':
			quoted = !quoted
			p.pos++
		case '{', '[':
			if !quoted {
				depth++
			}
			p.pos++
		case '}', ']':
			if quoted {
				p.pos++
				continue
			}
			if depth == 0 {
				return "", errors.New("ERR unsupported search query")
			}
			depth--
			p.pos++
		case ' ', '\t', '\n', '\r', '|', ')', '(':
			if !quoted && depth == 0 {
				if p.pos == start {
					return "", errors.New("ERR unsupported search query")
				}
				return strings.TrimSpace(p.query[start:p.pos]), nil
			}
			p.pos++
		default:
			p.pos++
		}
	}

	if depth != 0 || quoted || p.pos == start {
		return "", errors.New("ERR unsupported search query")
	}
	return strings.TrimSpace(p.query[start:p.pos]), nil
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

func parseSearchClause(part, fullQuery string) (searchQueryClause, error) {
	clause := searchQueryClause{}
	if !strings.HasPrefix(part, "@") {
		return searchQueryClause{}, errors.New("ERR unsupported search query")
	}

	colon := strings.IndexByte(part, ':')
	if colon <= 1 || colon == len(part)-1 {
		return searchQueryClause{}, errors.New("ERR unsupported search query")
	}

	clause.alias = part[1:colon]
	expr := part[colon+1:]

	if strings.HasPrefix(expr, "\"") && strings.HasSuffix(expr, "\"") {
		phrase := expr[1 : len(expr)-1]
		if strings.TrimSpace(phrase) == "" || strings.Contains(phrase, "\"") {
			return searchQueryClause{}, errors.New("ERR unsupported search query")
		}
		clause.textPhrase = &phrase
		return clause, nil
	}

	if strings.HasPrefix(expr, "{") && strings.HasSuffix(expr, "}") {
		value := expr[1 : len(expr)-1]
		if value == "" {
			return searchQueryClause{}, errors.New("ERR unsupported search query")
		}
		clause.tag = &value
		return clause, nil
	}

	if strings.HasPrefix(expr, "[") && strings.HasSuffix(expr, "]") {
		rangeBody := strings.TrimSpace(expr[1 : len(expr)-1])
		bounds := strings.Fields(rangeBody)
		if len(bounds) != 2 {
			return searchQueryClause{}, errors.New("ERR unsupported numeric range")
		}

		minimum, err := parseSearchBound(bounds[0])
		if err != nil {
			offset := strings.Index(fullQuery, bounds[0])
			return searchQueryClause{}, fmt.Errorf("SEARCH_SYNTAX Syntax error at offset %d near %s", offset, bounds[0])
		}
		maximum, err := parseSearchBound(bounds[1])
		if err != nil {
			offset := strings.Index(fullQuery, bounds[1])
			return searchQueryClause{}, fmt.Errorf("SEARCH_SYNTAX Syntax error at offset %d near %s", offset, bounds[1])
		}

		clause.minimum = &minimum
		clause.maximum = &maximum
		return clause, nil
	}

	if expr != "" && !strings.ContainsAny(expr, "{}[]()|") {
		prefix := strings.HasSuffix(expr, "*")
		if prefix {
			if strings.Count(expr, "*") != 1 || len(expr) == 1 {
				return searchQueryClause{}, errors.New("ERR unsupported search query")
			}
			expr = strings.TrimSuffix(expr, "*")
		} else if strings.Contains(expr, "*") {
			return searchQueryClause{}, errors.New("ERR unsupported search query")
		}
		value := strings.ToLower(expr)
		clause.text = &value
		clause.textPrefix = prefix
		return clause, nil
	}

	return searchQueryClause{}, errors.New("ERR unsupported search query")
}

func parseSearchQuery(query string, dialect int) (*searchQueryNode, error) {
	query = strings.TrimSpace(query)
	if query == "*" {
		return nil, nil
	}
	if query == "" {
		return nil, errors.New("ERR invalid search query")
	}

	if dialect == 1 {
		if err := validateSearchDialect1BooleanSyntax(query); err != nil {
			return nil, err
		}
	} else if dialect == 2 {
		if err := validateSearchDialect2BooleanSyntax(query); err != nil {
			return nil, err
		}
	}

	parser := searchQueryParser{query: query}
	return parser.parse()
}

func validateSearchDialect2BooleanSyntax(query string) error {
	type parenFrame struct {
		start int
	}

	stack := make([]parenFrame, 0, 4)
	bracketDepth := 0

	for i := 0; i < len(query); i++ {
		switch query[i] {
		case '{', '[':
			bracketDepth++
		case '}', ']':
			if bracketDepth == 0 {
				return errors.New("ERR unsupported search query")
			}
			bracketDepth--
		case '(':
			if bracketDepth == 0 {
				stack = append(stack, parenFrame{start: i})
			}
		case ')':
			if bracketDepth != 0 {
				continue
			}
			if len(stack) == 0 {
				return errors.New("ERR unsupported search query")
			}

			frame := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			content := strings.TrimSpace(query[frame.start+1 : i])

			parts := splitTopLevelSearchOR(content)
			if len(parts) > 1 {
				for _, part := range parts {
					if !isFullyParenthesizedSearchExpression(strings.TrimSpace(part)) {
						return errors.New("ERR unsupported search query")
					}
				}
			}
		}
	}

	if bracketDepth != 0 || len(stack) != 0 {
		return errors.New("ERR unsupported search query")
	}
	return nil
}

func splitTopLevelSearchOR(expr string) []string {
	parts := make([]string, 0, 2)
	start := 0
	parenDepth := 0
	bracketDepth := 0

	for i := 0; i < len(expr); i++ {
		switch expr[i] {
		case '{', '[':
			bracketDepth++
		case '}', ']':
			if bracketDepth > 0 {
				bracketDepth--
			}
		case '(':
			if bracketDepth == 0 {
				parenDepth++
			}
		case ')':
			if bracketDepth == 0 && parenDepth > 0 {
				parenDepth--
			}
		case '|':
			if bracketDepth == 0 && parenDepth == 0 {
				parts = append(parts, strings.TrimSpace(expr[start:i]))
				start = i + 1
			}
		}
	}

	if len(parts) == 0 {
		return nil
	}
	parts = append(parts, strings.TrimSpace(expr[start:]))
	return parts
}

func validateSearchDialect1BooleanSyntax(query string) error {
	depth := 0
	segmentStart := 0
	topLevelOR := false

	for i := 0; i < len(query); i++ {
		switch query[i] {
		case '{', '[':
			depth++
		case '}', ']':
			if depth == 0 {
				return errors.New("ERR unsupported search query")
			}
			depth--
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return errors.New("ERR unsupported search query")
			}
			depth--
		case '|':
			if depth != 0 {
				continue
			}
			topLevelOR = true
			if i == 0 || i+1 >= len(query) ||
				!isSearchSpace(query[i-1]) || !isSearchSpace(query[i+1]) {
				return errors.New("ERR unsupported search query")
			}
			if !isFullyParenthesizedSearchExpression(strings.TrimSpace(query[segmentStart:i])) {
				return errors.New("ERR unsupported search query")
			}
			segmentStart = i + 1
		}
	}

	if depth != 0 {
		return errors.New("ERR unsupported search query")
	}
	if topLevelOR && !isFullyParenthesizedSearchExpression(strings.TrimSpace(query[segmentStart:])) {
		return errors.New("ERR unsupported search query")
	}
	return nil
}

func isFullyParenthesizedSearchExpression(expr string) bool {
	if len(expr) < 2 || expr[0] != '(' || expr[len(expr)-1] != ')' {
		return false
	}

	depth := 0
	for i := 0; i < len(expr); i++ {
		switch expr[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return false
			}
			if depth == 0 && i != len(expr)-1 {
				return false
			}
		}
	}
	return depth == 0
}

func parseSearchOptions(args [][]byte) (searchOptions, error) {
	options := searchOptions{
		offset:  0,
		count:   10,
		dialect: 1,
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

		case "SORTBY":
			if pos+1 >= len(args) {
				return searchOptions{}, errors.New("ERR syntax error")
			}
			if options.sortBy != "" {
				return searchOptions{}, errors.New("ERR SORTBY specified more than once")
			}
			options.sortBy = string(args[pos+1])
			pos += 2
			if pos < len(args) {
				switch strings.ToUpper(string(args[pos])) {
				case "ASC":
					pos++
				case "DESC":
					options.sortDesc = true
					pos++
				}
			}

		case "DIALECT":
			if pos+1 >= len(args) {
				return searchOptions{}, errors.New("ERR syntax error")
			}
			dialect, err := strconv.Atoi(string(args[pos+1]))
			if err != nil || (dialect != 1 && dialect != 2) {
				return searchOptions{}, errors.New("ERR unsupported search dialect")
			}
			options.dialect = dialect
			pos += 2

		case "LANGUAGE":
			if pos+1 >= len(args) {
				return searchOptions{}, errors.New("ERR syntax error")
			}
			options.language = strings.ToLower(string(args[pos+1]))
			if !engine.SearchLanguageSupported(options.language) {
				return searchOptions{}, errors.New("SEARCH_QUERY_BAD No such language")
			}
			pos += 2

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

func searchKeySet(keys []string) map[string]struct{} {
	set := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		set[key] = struct{}{}
	}
	return set
}

func orderedSearchKeys(allKeys []string, set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for _, key := range allKeys {
		if _, ok := set[key]; ok {
			keys = append(keys, key)
		}
	}
	return keys
}

func evaluateSearchQuery(store *engine.Store, indexName string, node *searchQueryNode, allKeys []string, language string) ([]string, error) {
	if node == nil {
		return append([]string(nil), allKeys...), nil
	}

	switch node.kind {
	case searchQueryClauseNode:
		if node.clause == nil {
			return nil, errors.New("ERR unsupported search query")
		}
		switch {
		case node.clause.tag != nil:
			keys, ok := store.SearchTagKeys(indexName, node.clause.alias, *node.clause.tag)
			if !ok {
				return nil, errors.New("SEARCH_INDEX_NOT_FOUND Index not found: " + indexName)
			}
			return keys, nil

		case node.clause.textPhrase != nil:
			keys, ok := store.SearchTextPhraseKeys(indexName, node.clause.alias, *node.clause.textPhrase, language)
			if !ok {
				return nil, errors.New("SEARCH_INDEX_NOT_FOUND Index not found: " + indexName)
			}
			return keys, nil

		case node.clause.text != nil:
			var (
				keys []string
				ok   bool
			)
			if node.clause.textPrefix {
				keys, ok = store.SearchTextPrefixKeys(indexName, node.clause.alias, *node.clause.text)
			} else {
				keys, ok = store.SearchTextKeys(indexName, node.clause.alias, *node.clause.text, language)
			}
			if !ok {
				return nil, errors.New("SEARCH_INDEX_NOT_FOUND Index not found: " + indexName)
			}
			return keys, nil

		case node.clause.minimum != nil && node.clause.maximum != nil:
			keys, ok := store.SearchNumericRangeKeys(
				indexName,
				node.clause.alias,
				*node.clause.minimum,
				*node.clause.maximum,
			)
			if !ok {
				return nil, errors.New("SEARCH_INDEX_NOT_FOUND Index not found: " + indexName)
			}
			return keys, nil

		default:
			return nil, errors.New("ERR unsupported search query")
		}

	case searchQueryNotNode:
		child, err := evaluateSearchQuery(store, indexName, node.child, allKeys, language)
		if err != nil {
			return nil, err
		}
		excluded := searchKeySet(child)
		out := make([]string, 0, len(allKeys)-len(child))
		for _, key := range allKeys {
			if _, found := excluded[key]; !found {
				out = append(out, key)
			}
		}
		return out, nil

	case searchQueryAndNode:
		left, err := evaluateSearchQuery(store, indexName, node.left, allKeys, language)
		if err != nil {
			return nil, err
		}
		right, err := evaluateSearchQuery(store, indexName, node.right, allKeys, language)
		if err != nil {
			return nil, err
		}

		rightSet := searchKeySet(right)
		out := make([]string, 0)
		for _, key := range left {
			if _, ok := rightSet[key]; ok {
				out = append(out, key)
			}
		}
		return out, nil

	case searchQueryOrNode:
		left, err := evaluateSearchQuery(store, indexName, node.left, allKeys, language)
		if err != nil {
			return nil, err
		}
		right, err := evaluateSearchQuery(store, indexName, node.right, allKeys, language)
		if err != nil {
			return nil, err
		}

		union := searchKeySet(left)
		for _, key := range right {
			union[key] = struct{}{}
		}
		return orderedSearchKeys(allKeys, union), nil

	default:
		return nil, errors.New("ERR unsupported search query")
	}
}

func validateSearchTextAliases(def engine.SearchDefinition, node *searchQueryNode) error {
	if node == nil {
		return nil
	}

	switch node.kind {
	case searchQueryClauseNode:
		if node.clause == nil || (node.clause.text == nil && node.clause.textPhrase == nil) {
			return nil
		}
		for _, field := range def.Fields {
			if field.Alias != node.clause.alias {
				continue
			}
			if field.Kind != engine.SearchFieldText {
				return errors.New("ERR unsupported search query")
			}
			return nil
		}
		// Unknown field aliases preserve existing Search behavior: they simply
		// produce no matches rather than turning into a parser error.
		return nil

	case searchQueryAndNode, searchQueryOrNode:
		if err := validateSearchTextAliases(def, node.left); err != nil {
			return err
		}
		return validateSearchTextAliases(def, node.right)

	case searchQueryNotNode:
		return validateSearchTextAliases(def, node.child)

	default:
		return errors.New("ERR unsupported search query")
	}
}

func validateSearchPhraseStopwords(def engine.SearchDefinition, node *searchQueryNode, query string) error {
	if node == nil {
		return nil
	}

	switch node.kind {
	case searchQueryClauseNode:
		if node.clause == nil || node.clause.textPhrase == nil {
			return nil
		}
		for _, token := range strings.Fields(*node.clause.textPhrase) {
			if !engine.SearchIsStopword(def, token) {
				continue
			}
			offset := strings.Index(strings.ToLower(query), strings.ToLower(token))
			if offset < 0 {
				offset = 0
			}
			return fmt.Errorf("SEARCH_SYNTAX Syntax error at offset %d near %s", offset, token)
		}
		return nil

	case searchQueryAndNode, searchQueryOrNode:
		if err := validateSearchPhraseStopwords(def, node.left, query); err != nil {
			return err
		}
		return validateSearchPhraseStopwords(def, node.right, query)

	case searchQueryNotNode:
		return validateSearchPhraseStopwords(def, node.child, query)
	}

	return nil
}

func stripSearchStopwords(def engine.SearchDefinition, node *searchQueryNode) (*searchQueryNode, bool) {
	if node == nil {
		return nil, true
	}

	switch node.kind {
	case searchQueryClauseNode:
		if node.clause == nil {
			return node, true
		}
		if node.clause.text != nil && !node.clause.textPrefix &&
			engine.SearchIsStopword(def, *node.clause.text) {
			return nil, false
		}
		if node.clause.textPhrase != nil {
			tokens := strings.Fields(*node.clause.textPhrase)
			filtered := make([]string, 0, len(tokens))
			for _, token := range tokens {
				if engine.SearchIsStopword(def, token) {
					continue
				}
				filtered = append(filtered, token)
			}
			if len(filtered) == 0 {
				return nil, false
			}
			phrase := strings.Join(filtered, " ")
			copyNode := *node
			copyClause := *node.clause
			copyClause.textPhrase = &phrase
			copyNode.clause = &copyClause
			return &copyNode, true
		}
		return node, true

	case searchQueryAndNode, searchQueryOrNode:
		left, leftOK := stripSearchStopwords(def, node.left)
		right, rightOK := stripSearchStopwords(def, node.right)
		switch {
		case leftOK && rightOK:
			copyNode := *node
			copyNode.left = left
			copyNode.right = right
			return &copyNode, true
		case leftOK:
			return left, true
		case rightOK:
			return right, true
		default:
			return nil, false
		}

	case searchQueryNotNode:
		child, ok := stripSearchStopwords(def, node.child)
		if !ok {
			return nil, false
		}
		copyNode := *node
		copyNode.child = child
		return &copyNode, true

	default:
		return node, true
	}
}

func executeFTSearch(store *engine.Store, args [][]byte) ([]byte, error) {
	if len(args) < 3 {
		return nil, errors.New("ERR wrong number of arguments for 'ft.search' command")
	}

	indexName := string(args[1])
	options, err := parseSearchOptions(args)
	if err != nil {
		return nil, err
	}
	queryNode, err := parseSearchQuery(string(args[2]), options.dialect)
	if err != nil {
		return nil, err
	}

	def, ok := store.SearchDefinition(indexName)
	if !ok {
		return nil, errors.New("SEARCH_INDEX_NOT_FOUND Index not found: " + indexName)
	}
	language := def.Language
	if language == "" {
		language = "english"
	}
	if options.language != "" {
		language = options.language
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
			queryNode = &searchQueryNode{
				kind: searchQueryClauseNode,
				clause: &searchQueryClause{
					alias: "__snugkv_stopword_only__",
					text: func() *string {
						value := "__snugkv_no_match__"
						return &value
					}(),
				},
			}
		}
	}

	allKeys, ok := store.SearchAllKeys(indexName)
	if !ok {
		return nil, errors.New("SEARCH_INDEX_NOT_FOUND Index not found: " + indexName)
	}

	candidates := allKeys
	if queryNode != nil {
		matches, err := evaluateSearchQuery(store, indexName, queryNode, allKeys, language)
		if err != nil {
			return nil, err
		}
		candidates = matches
	}


	type hit struct {
		key        string
		raw        []byte
		sortFound  bool
		sortText   string
		sortNumber float64
	}

	var sortField *engine.SearchField
	if options.sortBy != "" {
		for i := range def.Fields {
			if def.Fields[i].Alias == options.sortBy {
				field := def.Fields[i]
				sortField = &field
				break
			}
		}
		if sortField == nil {
			return nil, errors.New("SEARCH_PROP_NOT_FOUND Property `" + options.sortBy + "` not loaded nor in schema")
		}
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

		item := hit{key: key, raw: raw}
		if sortField != nil {
			value, found, projectionErr := store.JSONProjection(key, sortField.Path)
			if projectionErr != nil {
				return nil, projectionErr
			}
			if found {
				item.sortFound = true
				switch sortField.Kind {
				case engine.SearchFieldNumeric:
					number, parseErr := strconv.ParseFloat(string(value), 64)
					if parseErr != nil || math.IsNaN(number) || math.IsInf(number, 0) {
						item.sortFound = false
					} else {
						item.sortNumber = number
					}
				case engine.SearchFieldTag:
					item.sortText = string(value)
				}
			}
		}
		hits = append(hits, item)
	}

	if sortField != nil {
		sort.SliceStable(hits, func(i, j int) bool {
			left, right := hits[i], hits[j]

			// Keep documents with missing/unusable sort values after documents
			// with values in both directions. Redis's exact missing-value
			// placement is covered by the differential harness before merge.
			if left.sortFound != right.sortFound {
				return left.sortFound
			}

			var less bool
			var equal bool
			if !left.sortFound {
				equal = true
			} else if sortField.Kind == engine.SearchFieldNumeric {
				less = left.sortNumber < right.sortNumber
				equal = left.sortNumber == right.sortNumber
			} else {
				less = left.sortText < right.sortText
				equal = left.sortText == right.sortText
			}

			if equal {
				return left.key < right.key
			}
			if options.sortDesc {
				return !less
			}
			return less
		})
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
