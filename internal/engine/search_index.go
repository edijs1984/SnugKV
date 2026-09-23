package engine

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"snugkv/internal/jsonvalue"
)

type SearchFieldKind uint8

const (
	SearchFieldTag SearchFieldKind = iota
	SearchFieldNumeric
	SearchFieldText
)

type SearchField struct {
	Path   string
	Alias  string
	Kind   SearchFieldKind
	NoStem bool
}

type SearchDefinition struct {
	Name                string
	Prefixes            []string
	Fields              []SearchField
	Stopwords           []string
	StopwordsConfigured bool
	Language            string
	LanguageField       string
}

type numericPosting struct {
	Key   string
	Value float64
}

type searchDocumentState struct {
	Tags          map[string][]string
	Numerics      map[string][]float64
	Texts         map[string][]string
	TextStems     map[string][]string
	TextSequences map[string][][]string
}

type searchIndex struct {
	def SearchDefinition

	docs map[string]searchDocumentState

	tags      map[string]map[string]map[string]struct{}
	texts     map[string]map[string]map[string]struct{}
	textStems map[string]map[string]map[string]struct{}

	numerics      map[string]map[string][]float64
	numericSorted map[string][]numericPosting
	numericDirty  map[string]bool
}

type searchManager struct {
	mu      sync.RWMutex
	indexes map[string]*searchIndex
}

func newSearchManager() *searchManager {
	return &searchManager{indexes: make(map[string]*searchIndex)}
}

func cloneSearchDefinition(def SearchDefinition) SearchDefinition {
	return SearchDefinition{
		Name:                def.Name,
		Prefixes:            append([]string(nil), def.Prefixes...),
		Fields:              append([]SearchField(nil), def.Fields...),
		Stopwords:           append([]string(nil), def.Stopwords...),
		StopwordsConfigured: def.StopwordsConfigured,
		Language:            def.Language,
		LanguageField:       def.LanguageField,
	}
}

func newSearchIndex(def SearchDefinition) *searchIndex {
	return &searchIndex{
		def:           cloneSearchDefinition(def),
		docs:          make(map[string]searchDocumentState),
		tags:          make(map[string]map[string]map[string]struct{}),
		texts:         make(map[string]map[string]map[string]struct{}),
		textStems:     make(map[string]map[string]map[string]struct{}),
		numerics:      make(map[string]map[string][]float64),
		numericSorted: make(map[string][]numericPosting),
		numericDirty:  make(map[string]bool),
	}
}

func normalizeSearchLanguage(language string) string {
	if language == "" {
		return "english"
	}
	return strings.ToLower(language)
}

func SearchLanguageSupported(language string) bool {
	switch normalizeSearchLanguage(language) {
	case "english", "german":
		return true
	default:
		return false
	}
}

func stemSearchLanguage(language, token string) string {
	switch normalizeSearchLanguage(language) {
	case "german":
		return stemSearchGerman(token)
	default:
		return stemSearchEnglish(token)
	}
}

func effectiveSearchDocumentLanguage(def SearchDefinition, root any) string {
	language := normalizeSearchLanguage(def.Language)
	if def.LanguageField == "" {
		return language
	}
	values, err := jsonvalue.Matches(root, def.LanguageField)
	if err != nil || len(values) == 0 {
		return language
	}
	if value, ok := values[0].(string); ok && SearchLanguageSupported(value) {
		return normalizeSearchLanguage(value)
	}
	return language
}

func validateSearchDefinition(def SearchDefinition) error {
	if def.Name == "" {
		return errors.New("ERR search index name is required")
	}
	if len(def.Fields) == 0 {
		return errors.New("ERR search schema is required")
	}
	if !SearchLanguageSupported(def.Language) {
		return errors.New("SEARCH_ADD_ARGS Invalid language")
	}

	aliases := make(map[string]struct{}, len(def.Fields))
	for _, field := range def.Fields {
		if field.Path == "" || field.Alias == "" {
			return errors.New("ERR search field path and alias are required")
		}
		if _, exists := aliases[field.Alias]; exists {
			return errors.New("SEARCH_QUERY_BAD Duplicate field in schema - " + field.Alias)
		}
		aliases[field.Alias] = struct{}{}

		switch field.Kind {
		case SearchFieldTag, SearchFieldNumeric, SearchFieldText:
		default:
			return errors.New("ERR unsupported search field type")
		}
	}
	return nil
}

func normalizeSearchPrefixes(prefixes []string) []string {
	if len(prefixes) == 0 {
		return []string{""}
	}
	out := append([]string(nil), prefixes...)
	sort.Strings(out)
	return out
}

func searchPrefixMatches(prefixes []string, key string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func canonicalSearchTag(value any) (string, bool) {
	switch value := value.(type) {
	case string:
		return value, true
	case bool:
		if value {
			return "true", true
		}
		return "false", true
	case float64:
		return strconv.FormatFloat(value, 'g', -1, 64), true
	default:
		return "", false
	}
}

var defaultSearchStopwords = map[string]struct{}{
	"a": {}, "is": {}, "the": {}, "an": {}, "and": {}, "are": {}, "as": {},
	"at": {}, "be": {}, "but": {}, "by": {}, "for": {}, "if": {}, "in": {},
	"into": {}, "it": {}, "no": {}, "not": {}, "of": {}, "on": {}, "or": {},
	"such": {}, "that": {}, "their": {}, "then": {}, "there": {}, "these": {},
	"they": {}, "this": {}, "to": {}, "was": {}, "will": {}, "with": {},
}

func normalizeSearchStopwords(def *SearchDefinition) {
	for i := range def.Stopwords {
		def.Stopwords[i] = strings.ToLower(def.Stopwords[i])
	}
	def.Stopwords = uniqueStrings(def.Stopwords)
}

func SearchIsStopword(def SearchDefinition, token string) bool {
	token = strings.ToLower(token)
	if def.StopwordsConfigured {
		for _, stopword := range def.Stopwords {
			if stopword == token {
				return true
			}
		}
		return false
	}
	_, ok := defaultSearchStopwords[token]
	return ok
}

func filterSearchStopwords(def SearchDefinition, tokens []string) []string {
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if SearchIsStopword(def, token) {
			continue
		}
		out = append(out, token)
	}
	return out
}

func tokenizeSearchTextSequence(value string) []string {
	tokens := make([]string, 0, 8)
	var current []rune

	flush := func() {
		if len(current) == 0 {
			return
		}
		tokens = append(tokens, strings.ToLower(string(current)))
		current = current[:0]
	}

	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			current = append(current, r)
			continue
		}
		flush()
	}
	flush()

	return tokens
}

func tokenizeSearchText(value string) []string {
	return uniqueStrings(tokenizeSearchTextSequence(value))
}

func uniqueStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func uniqueFloat64s(values []float64) []float64 {
	if len(values) < 2 {
		return values
	}
	sort.Float64s(values)
	out := values[:0]
	for _, value := range values {
		if len(out) > 0 && out[len(out)-1] == value {
			continue
		}
		out = append(out, value)
	}
	return out
}

func extractSearchDocument(def SearchDefinition, raw []byte) (searchDocumentState, error) {
	root, err := jsonvalue.Parse(raw)
	if err != nil {
		return searchDocumentState{}, err
	}

	language := effectiveSearchDocumentLanguage(def, root)

	state := searchDocumentState{
		Tags:          make(map[string][]string),
		Numerics:      make(map[string][]float64),
		Texts:         make(map[string][]string),
		TextStems:     make(map[string][]string),
		TextSequences: make(map[string][][]string),
	}

	for _, field := range def.Fields {
		values, err := jsonvalue.Matches(root, field.Path)
		if err != nil {
			// Redis accepts malformed JSONPath strings at FT.CREATE time.
			// Treat an unusable schema path as producing no indexed values
			// instead of failing backfill or later document mutations.
			continue
		}

		switch field.Kind {
		case SearchFieldTag:
			tags := make([]string, 0, len(values))
			for _, value := range values {
				if canonical, ok := canonicalSearchTag(value); ok {
					tags = append(tags, canonical)
				}
			}
			if len(tags) > 0 {
				state.Tags[field.Alias] = uniqueStrings(tags)
			}

		case SearchFieldText:
			tokens := make([]string, 0)
			stems := make([]string, 0)
			sequences := make([][]string, 0, len(values))
			for _, value := range values {
				text, ok := value.(string)
				if !ok {
					continue
				}
				sequence := filterSearchStopwords(def, tokenizeSearchTextSequence(text))
				if len(sequence) == 0 {
					continue
				}
				sequences = append(sequences, sequence)
				tokens = append(tokens, sequence...)
				if !field.NoStem {
					for _, token := range sequence {
						stems = append(stems, stemSearchLanguage(language, token))
					}
				}
			}
			if len(tokens) > 0 {
				state.Texts[field.Alias] = uniqueStrings(tokens)
				if len(stems) > 0 {
					state.TextStems[field.Alias] = uniqueStrings(stems)
				}
				state.TextSequences[field.Alias] = sequences
			}

		case SearchFieldNumeric:
			numbers := make([]float64, 0, len(values))
			for _, value := range values {
				number, ok := value.(float64)
				if !ok {
					continue
				}
				numbers = append(numbers, number)
			}
			if len(numbers) > 0 {
				state.Numerics[field.Alias] = uniqueFloat64s(numbers)
			}
		}
	}

	return state, nil
}

func (m *searchManager) create(def SearchDefinition) error {
	if err := validateSearchDefinition(def); err != nil {
		return err
	}
	def = cloneSearchDefinition(def)
	def.Prefixes = normalizeSearchPrefixes(def.Prefixes)
	def.Language = normalizeSearchLanguage(def.Language)
	normalizeSearchStopwords(&def)

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.indexes[def.Name]; exists {
		return errors.New("ERR search index already exists")
	}
	m.indexes[def.Name] = newSearchIndex(def)
	return nil
}

func (m *searchManager) drop(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.indexes[name]; !exists {
		return false
	}
	delete(m.indexes, name)
	return true
}

func (m *searchManager) names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.indexes))
	for name := range m.indexes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}


func (m *searchManager) definitions() []SearchDefinition {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.indexes))
	for name := range m.indexes {
		names = append(names, name)
	}
	sort.Strings(names)

	defs := make([]SearchDefinition, 0, len(names))
	for _, name := range names {
		defs = append(defs, cloneSearchDefinition(m.indexes[name].def))
	}
	return defs
}

func (idx *searchIndex) removeDocument(key string) {
	old, exists := idx.docs[key]
	if !exists {
		return
	}

	for alias, values := range old.Tags {
		field := idx.tags[alias]
		for _, value := range values {
			postings := field[value]
			delete(postings, key)
			if len(postings) == 0 {
				delete(field, value)
			}
		}
		if len(field) == 0 {
			delete(idx.tags, alias)
		}
	}

	for alias, values := range old.Texts {
		field := idx.texts[alias]
		for _, value := range values {
			postings := field[value]
			delete(postings, key)
			if len(postings) == 0 {
				delete(field, value)
			}
		}
		if len(field) == 0 {
			delete(idx.texts, alias)
		}
	}

	for alias, values := range old.TextStems {
		field := idx.textStems[alias]
		for _, value := range values {
			postings := field[value]
			delete(postings, key)
			if len(postings) == 0 {
				delete(field, value)
			}
		}
		if len(field) == 0 {
			delete(idx.textStems, alias)
		}
	}

	for alias := range old.Numerics {
		if field := idx.numerics[alias]; field != nil {
			delete(field, key)
			if len(field) == 0 {
				delete(idx.numerics, alias)
			}
		}
		idx.numericDirty[alias] = true
	}

	delete(idx.docs, key)
}

func (idx *searchIndex) addDocument(key string, state searchDocumentState) {
	if len(state.Tags) == 0 && len(state.Numerics) == 0 && len(state.Texts) == 0 {
		return
	}

	idx.docs[key] = state

	for alias, values := range state.Tags {
		field := idx.tags[alias]
		if field == nil {
			field = make(map[string]map[string]struct{})
			idx.tags[alias] = field
		}
		for _, value := range values {
			postings := field[value]
			if postings == nil {
				postings = make(map[string]struct{})
				field[value] = postings
			}
			postings[key] = struct{}{}
		}
	}

	for alias, values := range state.Texts {
		field := idx.texts[alias]
		if field == nil {
			field = make(map[string]map[string]struct{})
			idx.texts[alias] = field
		}
		for _, value := range values {
			postings := field[value]
			if postings == nil {
				postings = make(map[string]struct{})
				field[value] = postings
			}
			postings[key] = struct{}{}
		}
	}

	for alias, values := range state.TextStems {
		field := idx.textStems[alias]
		if field == nil {
			field = make(map[string]map[string]struct{})
			idx.textStems[alias] = field
		}
		for _, value := range values {
			postings := field[value]
			if postings == nil {
				postings = make(map[string]struct{})
				field[value] = postings
			}
			postings[key] = struct{}{}
		}
	}

	for alias, values := range state.Numerics {
		field := idx.numerics[alias]
		if field == nil {
			field = make(map[string][]float64)
			idx.numerics[alias] = field
		}
		field[key] = append([]float64(nil), values...)
		idx.numericDirty[alias] = true
	}
}

func (idx *searchIndex) replaceJSON(key string, raw []byte) error {
	idx.removeDocument(key)
	if !searchPrefixMatches(idx.def.Prefixes, key) {
		return nil
	}

	state, err := extractSearchDocument(idx.def, raw)
	if err != nil {
		return err
	}
	idx.addDocument(key, state)
	return nil
}

func (m *searchManager) replaceJSON(key string, raw []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, idx := range m.indexes {
		if err := idx.replaceJSON(key, raw); err != nil {
			return err
		}
	}
	return nil
}

func (m *searchManager) removeKey(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, idx := range m.indexes {
		idx.removeDocument(key)
	}
}

func sortedPostingKeys(postings map[string]struct{}) []string {
	keys := make([]string, 0, len(postings))
	for key := range postings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}



func (m *searchManager) allKeys(indexName string) ([]string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	idx, ok := m.indexes[indexName]
	if !ok {
		return nil, false
	}

	keys := make([]string, 0, len(idx.docs))
	for key := range idx.docs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, true
}

func (m *searchManager) tagKeys(indexName, alias, value string) ([]string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	idx, ok := m.indexes[indexName]
	if !ok {
		return nil, false
	}
	field := idx.tags[alias]
	if field == nil {
		return []string{}, true
	}
	return sortedPostingKeys(field[value]), true
}

func searchTextField(def SearchDefinition, alias string) (SearchField, bool) {
	for _, field := range def.Fields {
		if field.Alias == alias && field.Kind == SearchFieldText {
			return field, true
		}
	}
	return SearchField{}, false
}

func (m *searchManager) textKeys(indexName, alias, token, language string) ([]string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	idx, ok := m.indexes[indexName]
	if !ok {
		return nil, false
	}
	field := idx.texts[alias]
	if field == nil {
		return []string{}, true
	}

	token = strings.ToLower(token)
	seen := make(map[string]struct{})
	for key := range field[token] {
		seen[key] = struct{}{}
	}

	defField, found := searchTextField(idx.def, alias)
	if found && !defField.NoStem {
		stem := stemSearchLanguage(language, token)
		for key := range idx.textStems[alias][stem] {
			seen[key] = struct{}{}
		}
	}
	return sortedPostingKeys(seen), true
}


func searchEditDistanceWithin(a, b string, maxDistance int) bool {
	ar := []rune(a)
	br := []rune(b)
	if maxDistance < 0 {
		return false
	}
	diff := len(ar) - len(br)
	if diff < 0 {
		diff = -diff
	}
	if diff > maxDistance {
		return false
	}

	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		rowMin := curr[0]
		for j := 1; j <= len(br); j++ {
			cost := 0
			if ar[i-1] != br[j-1] {
				cost = 1
			}
			deleteCost := prev[j] + 1
			insertCost := curr[j-1] + 1
			replaceCost := prev[j-1] + cost
			best := deleteCost
			if insertCost < best {
				best = insertCost
			}
			if replaceCost < best {
				best = replaceCost
			}
			curr[j] = best
			if best < rowMin {
				rowMin = best
			}
		}
		if rowMin > maxDistance {
			return false
		}
		prev, curr = curr, prev
	}
	return prev[len(br)] <= maxDistance
}

func (m *searchManager) textFuzzyKeys(indexName, alias, token string, distance int) ([]string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	idx, ok := m.indexes[indexName]
	if !ok {
		return nil, false
	}
	field := idx.texts[alias]
	if field == nil {
		return []string{}, true
	}

	token = strings.ToLower(token)
	seen := make(map[string]struct{})
	for candidate, postings := range field {
		if !searchEditDistanceWithin(token, candidate, distance) {
			continue
		}
		for key := range postings {
			seen[key] = struct{}{}
		}
	}

	defField, found := searchTextField(idx.def, alias)
	if found && !defField.NoStem {
		for candidate, postings := range idx.textStems[alias] {
			if !searchEditDistanceWithin(token, candidate, distance) {
				continue
			}
			for key := range postings {
				seen[key] = struct{}{}
			}
		}
	}
	return sortedPostingKeys(seen), true
}

func (m *searchManager) textPrefixKeys(indexName, alias, prefix string) ([]string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	idx, ok := m.indexes[indexName]
	if !ok {
		return nil, false
	}
	field := idx.texts[alias]
	if field == nil {
		return []string{}, true
	}

	prefix = strings.ToLower(prefix)
	seen := make(map[string]struct{})
	for token, postings := range field {
		if !strings.HasPrefix(token, prefix) {
			continue
		}
		for key := range postings {
			seen[key] = struct{}{}
		}
	}
	return sortedPostingKeys(seen), true
}

func containsSearchPhrase(sequence, phrase []string) bool {
	if len(phrase) == 0 || len(sequence) < len(phrase) {
		return false
	}
	for start := 0; start+len(phrase) <= len(sequence); start++ {
		matched := true
		for i := range phrase {
			if sequence[start+i] != phrase[i] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func stemSearchSequence(tokens []string, language string) []string {
	out := make([]string, len(tokens))
	for i, token := range tokens {
		out[i] = stemSearchLanguage(language, token)
	}
	return out
}

func (m *searchManager) textPhraseKeys(indexName, alias, phrase, language string) ([]string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	idx, ok := m.indexes[indexName]
	if !ok {
		return nil, false
	}

	tokens := filterSearchStopwords(idx.def, tokenizeSearchTextSequence(phrase))
	if len(tokens) == 0 {
		return []string{}, true
	}

	defField, found := searchTextField(idx.def, alias)
	useStems := found && !defField.NoStem
	if useStems {
		tokens = stemSearchSequence(tokens, language)
	}

	matches := make(map[string]struct{})
	for key, state := range idx.docs {
		for _, sequence := range state.TextSequences[alias] {
			candidate := sequence
			if useStems {
				candidate = stemSearchSequence(sequence, language)
			}
			if containsSearchPhrase(candidate, tokens) {
				matches[key] = struct{}{}
				break
			}
		}
	}
	return sortedPostingKeys(matches), true
}


func searchTermsWithinSlop(sequence, terms []string, slop int, inOrder bool) bool {
	if len(terms) < 2 || len(sequence) < len(terms) {
		return false
	}
	positions := make([]int, len(terms))
	used := make([]bool, len(sequence))

	var visit func(int) bool
	visit = func(termIndex int) bool {
		if termIndex == len(terms) {
			minPos, maxPos := positions[0], positions[0]
			for _, pos := range positions[1:] {
				if pos < minPos {
					minPos = pos
				}
				if pos > maxPos {
					maxPos = pos
				}
			}
			if maxPos-minPos-(len(terms)-1) > slop {
				return false
			}
			if inOrder {
				for j := 1; j < len(positions); j++ {
					if positions[j] <= positions[j-1] {
						return false
					}
				}
			}
			return true
		}

		for pos, token := range sequence {
			if used[pos] || token != terms[termIndex] {
				continue
			}
			used[pos] = true
			positions[termIndex] = pos
			if visit(termIndex + 1) {
				return true
			}
			used[pos] = false
		}
		return false
	}
	return visit(0)
}

func (m *searchManager) textProximityKeys(indexName, alias string, terms []string, slop int, inOrder bool, language string) ([]string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	idx, ok := m.indexes[indexName]
	if !ok {
		return nil, false
	}
	if slop < 0 {
		slop = 0
	}

	filtered := filterSearchStopwords(idx.def, terms)
	if len(filtered) < 2 {
		return []string{}, true
	}

	defField, found := searchTextField(idx.def, alias)
	useStems := found && !defField.NoStem
	if useStems {
		filtered = stemSearchSequence(filtered, language)
	}

	matches := make(map[string]struct{})
	for key, state := range idx.docs {
		for _, sequence := range state.TextSequences[alias] {
			candidate := sequence
			if useStems {
				candidate = stemSearchSequence(sequence, language)
			}
			if searchTermsWithinSlop(candidate, filtered, slop, inOrder) {
				matches[key] = struct{}{}
				break
			}
		}
	}
	return sortedPostingKeys(matches), true
}

func (idx *searchIndex) ensureNumericSorted(alias string) []numericPosting {
	if !idx.numericDirty[alias] {
		return idx.numericSorted[alias]
	}

	field := idx.numerics[alias]
	postings := make([]numericPosting, 0)
	for key, values := range field {
		for _, value := range values {
			postings = append(postings, numericPosting{Key: key, Value: value})
		}
	}

	sort.Slice(postings, func(i, j int) bool {
		if postings[i].Value == postings[j].Value {
			return postings[i].Key < postings[j].Key
		}
		return postings[i].Value < postings[j].Value
	})

	idx.numericSorted[alias] = postings
	idx.numericDirty[alias] = false
	return postings
}

func (m *searchManager) numericRangeKeys(indexName, alias string, min, max float64) ([]string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx, ok := m.indexes[indexName]
	if !ok {
		return nil, false
	}
	if min > max {
		return []string{}, true
	}

	postings := idx.ensureNumericSorted(alias)
	seen := make(map[string]struct{})
	for _, posting := range postings {
		if posting.Value < min {
			continue
		}
		if posting.Value > max {
			break
		}
		seen[posting.Key] = struct{}{}
	}
	return sortedPostingKeys(seen), true
}

func intersectSearchKeys(sets ...[]string) []string {
	if len(sets) == 0 {
		return []string{}
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

	out := make([]string, 0)
	for key, count := range counts {
		if count == len(sets) {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func (m *searchManager) memoryBytes() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var bytes uint64
	for name, idx := range m.indexes {
		bytes += uint64(len(name))
		for _, prefix := range idx.def.Prefixes {
			bytes += uint64(len(prefix))
		}
		for _, field := range idx.def.Fields {
			bytes += uint64(len(field.Path) + len(field.Alias))
		}
		for key, state := range idx.docs {
			bytes += uint64(len(key))
			for alias, values := range state.Tags {
				bytes += uint64(len(alias))
				for _, value := range values {
					bytes += uint64(len(value) + len(key))
				}
			}
			for alias, values := range state.Texts {
				bytes += uint64(len(alias))
				for _, value := range values {
					bytes += uint64(len(value) + len(key))
				}
			}
			for alias, values := range state.TextStems {
				bytes += uint64(len(alias))
				for _, value := range values {
					bytes += uint64(len(value) + len(key))
				}
			}
			for alias, sequences := range state.TextSequences {
				bytes += uint64(len(alias))
				for _, sequence := range sequences {
					for _, token := range sequence {
						bytes += uint64(len(token))
					}
				}
			}
			for alias, values := range state.Numerics {
				bytes += uint64(len(alias) + len(key))
				bytes += uint64(len(values)) * 8
			}
		}
	}
	return bytes
}


func (s *Store) getSearchManager() *searchManager {
	return s.search.Load()
}

func (s *Store) ensureSearchManager() *searchManager {
	if current := s.search.Load(); current != nil {
		return current
	}

	created := newSearchManager()
	if s.search.CompareAndSwap(nil, created) {
		return created
	}
	return s.search.Load()
}

func (s *Store) CreateSearchIndex(def SearchDefinition) error {
	if err := validateSearchDefinition(def); err != nil {
		return err
	}
	def = cloneSearchDefinition(def)
	def.Prefixes = normalizeSearchPrefixes(def.Prefixes)
	def.Language = normalizeSearchLanguage(def.Language)
	normalizeSearchStopwords(&def)

	manager := s.ensureSearchManager()

	// Build the new index while all primary shards are locked, then publish it
	// into the manager only after backfill is complete. This prevents queries
	// from observing a partially built index and gives mutations a clean
	// before/after publication boundary.
	unlock := s.lockAll()
	defer unlock()

	manager.mu.Lock()
	defer manager.mu.Unlock()

	if _, exists := manager.indexes[def.Name]; exists {
		return errors.New("ERR search index already exists")
	}

	idx := newSearchIndex(def)
	now := s.now()
	for i := range s.shards {
		sh := &s.shards[i]
		for key, entry := range sh.all() {
			if sh.expired(key, entry, now) || entry.valueType != TypeJSON {
				continue
			}
			if err := idx.replaceJSON(key, s.decode(sh, entry)); err != nil {
				return err
			}
		}
	}

	manager.indexes[def.Name] = idx
	return nil
}

func (s *Store) SearchDefinitions() []SearchDefinition {
	manager := s.getSearchManager()
	if manager == nil {
		return []SearchDefinition{}
	}
	return manager.definitions()
}


func (s *Store) SearchDefinition(name string) (SearchDefinition, bool) {
	manager := s.getSearchManager()
	if manager == nil {
		return SearchDefinition{}, false
	}

	manager.mu.RLock()
	defer manager.mu.RUnlock()

	idx, ok := manager.indexes[name]
	if !ok {
		return SearchDefinition{}, false
	}
	return cloneSearchDefinition(idx.def), true
}


func (s *Store) RestoreSearchDefinitions(defs []SearchDefinition) error {
	normalized := make([]SearchDefinition, len(defs))
	seen := make(map[string]struct{}, len(defs))
	for i, def := range defs {
		if err := validateSearchDefinition(def); err != nil {
			return err
		}
		if _, exists := seen[def.Name]; exists {
			return errors.New("ERR duplicate search index name")
		}
		seen[def.Name] = struct{}{}
		normalized[i] = cloneSearchDefinition(def)
		normalized[i].Prefixes = normalizeSearchPrefixes(normalized[i].Prefixes)
		normalized[i].Language = normalizeSearchLanguage(normalized[i].Language)
		normalizeSearchStopwords(&normalized[i])
	}

	next := newSearchManager()
	for _, def := range normalized {
		next.indexes[def.Name] = newSearchIndex(def)
	}

	unlock := s.lockAll()
	defer unlock()

	now := s.now()
	for i := range s.shards {
		sh := &s.shards[i]
		for key, entry := range sh.all() {
			if sh.expired(key, entry, now) || entry.valueType != TypeJSON {
				continue
			}
			raw := s.decode(sh, entry)
			for _, idx := range next.indexes {
				if err := idx.replaceJSON(key, raw); err != nil {
					return err
				}
			}
		}
	}

	if len(next.indexes) == 0 {
		s.search.Store(nil)
	} else {
		s.search.Store(next)
	}
	return nil
}

func (s *Store) DropSearchIndex(name string) bool {
	manager := s.getSearchManager()
	if manager == nil {
		return false
	}
	return manager.drop(name)
}

func (s *Store) SearchIndexNames() []string {
	manager := s.getSearchManager()
	if manager == nil {
		return []string{}
	}
	return manager.names()
}



func (s *Store) SearchAllKeys(indexName string) ([]string, bool) {
	manager := s.getSearchManager()
	if manager == nil {
		return nil, false
	}
	return manager.allKeys(indexName)
}

func (s *Store) SearchTagKeys(indexName, alias, value string) ([]string, bool) {
	manager := s.getSearchManager()
	if manager == nil {
		return nil, false
	}
	return manager.tagKeys(indexName, alias, value)
}

func (s *Store) SearchTextKeys(indexName, alias, token, language string) ([]string, bool) {
	manager := s.getSearchManager()
	if manager == nil {
		return nil, false
	}
	return manager.textKeys(indexName, alias, token, language)
}

func (s *Store) SearchTextPrefixKeys(indexName, alias, prefix string) ([]string, bool) {
	manager := s.getSearchManager()
	if manager == nil {
		return nil, false
	}
	return manager.textPrefixKeys(indexName, alias, prefix)
}

func (s *Store) SearchTextFuzzyKeys(indexName, alias, token string, distance int) ([]string, bool) {
	manager := s.getSearchManager()
	if manager == nil {
		return nil, false
	}
	return manager.textFuzzyKeys(indexName, alias, token, distance)
}

func (s *Store) SearchTextPhraseKeys(indexName, alias, phrase, language string) ([]string, bool) {
	manager := s.getSearchManager()
	if manager == nil {
		return nil, false
	}
	return manager.textPhraseKeys(indexName, alias, phrase, language)
}

func (s *Store) SearchTextProximityKeys(indexName, alias string, terms []string, slop int, inOrder bool, language string) ([]string, bool) {
	manager := s.getSearchManager()
	if manager == nil {
		return nil, false
	}
	return manager.textProximityKeys(indexName, alias, terms, slop, inOrder, language)
}

func (s *Store) SearchNumericRangeKeys(indexName, alias string, min, max float64) ([]string, bool) {
	manager := s.getSearchManager()
	if manager == nil {
		return nil, false
	}
	return manager.numericRangeKeys(indexName, alias, min, max)
}

func (s *Store) SearchMemoryBytes() uint64 {
	manager := s.getSearchManager()
	if manager == nil {
		return 0
	}
	return manager.memoryBytes()
}

func (s *Store) searchReplaceJSON(key string, raw []byte) error {
	manager := s.getSearchManager()
	if manager == nil {
		return nil
	}
	return manager.replaceJSON(key, raw)
}

func (s *Store) searchRemoveKey(key string) {
	manager := s.getSearchManager()
	if manager == nil {
		return
	}
	manager.removeKey(key)
}
