package engine

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"

	"snugkv/internal/jsonvalue"
)

type SearchFieldKind uint8

const (
	SearchFieldTag SearchFieldKind = iota
	SearchFieldNumeric
)

type SearchField struct {
	Path  string
	Alias string
	Kind  SearchFieldKind
}

type SearchDefinition struct {
	Name     string
	Prefixes []string
	Fields   []SearchField
}

type numericPosting struct {
	Key   string
	Value float64
}

type searchDocumentState struct {
	Tags     map[string][]string
	Numerics map[string][]float64
}

type searchIndex struct {
	def SearchDefinition

	docs map[string]searchDocumentState

	tags map[string]map[string]map[string]struct{}

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

func newSearchIndex(def SearchDefinition) *searchIndex {
	return &searchIndex{
		def:           def,
		docs:          make(map[string]searchDocumentState),
		tags:          make(map[string]map[string]map[string]struct{}),
		numerics:      make(map[string]map[string][]float64),
		numericSorted: make(map[string][]numericPosting),
		numericDirty:  make(map[string]bool),
	}
}

func validateSearchDefinition(def SearchDefinition) error {
	if def.Name == "" {
		return errors.New("ERR search index name is required")
	}
	if len(def.Fields) == 0 {
		return errors.New("ERR search schema is required")
	}

	aliases := make(map[string]struct{}, len(def.Fields))
	for _, field := range def.Fields {
		if field.Path == "" || field.Alias == "" {
			return errors.New("ERR search field path and alias are required")
		}
		if _, err := jsonvalue.Matches(map[string]any{}, field.Path); err != nil {
			return err
		}
		if _, exists := aliases[field.Alias]; exists {
			return errors.New("ERR duplicate search field alias")
		}
		aliases[field.Alias] = struct{}{}

		switch field.Kind {
		case SearchFieldTag, SearchFieldNumeric:
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

	state := searchDocumentState{
		Tags:     make(map[string][]string),
		Numerics: make(map[string][]float64),
	}

	for _, field := range def.Fields {
		values, err := jsonvalue.Matches(root, field.Path)
		if err != nil {
			return searchDocumentState{}, err
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
	def.Prefixes = normalizeSearchPrefixes(def.Prefixes)

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
	if len(state.Tags) == 0 && len(state.Numerics) == 0 {
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

func (m *searchManager) replaceJSON(key string, raw []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, idx := range m.indexes {
		idx.removeDocument(key)
		if !searchPrefixMatches(idx.def.Prefixes, key) {
			continue
		}

		state, err := extractSearchDocument(idx.def, raw)
		if err != nil {
			return err
		}
		idx.addDocument(key, state)
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
	return s.ensureSearchManager().create(def)
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

func (s *Store) SearchTagKeys(indexName, alias, value string) ([]string, bool) {
	manager := s.getSearchManager()
	if manager == nil {
		return nil, false
	}
	return manager.tagKeys(indexName, alias, value)
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
