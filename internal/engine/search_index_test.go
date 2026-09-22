package engine

import (
	"reflect"
	"testing"
)

func testSearchDefinition() searchDefinition {
	return searchDefinition{
		Name:     "products",
		Prefixes: []string{"product:"},
		Fields: []searchField{
			{Path: "$.category", Alias: "category", Kind: searchFieldTag},
			{Path: "$.price", Alias: "price", Kind: searchFieldNumeric},
			{Path: "$.tags[*]", Alias: "tags", Kind: searchFieldTag},
			{Path: "$.variants[*].price", Alias: "variant_price", Kind: searchFieldNumeric},
		},
	}
}

func TestSearchDefinitionValidation(t *testing.T) {
	if err := validateSearchDefinition(testSearchDefinition()); err != nil {
		t.Fatal(err)
	}

	duplicate := testSearchDefinition()
	duplicate.Fields = append(duplicate.Fields,
		searchField{Path: "$.other", Alias: "price", Kind: searchFieldTag},
	)
	if err := validateSearchDefinition(duplicate); err == nil {
		t.Fatal("expected duplicate alias error")
	}

	invalidPath := testSearchDefinition()
	invalidPath.Fields[0].Path = "$["
	if err := validateSearchDefinition(invalidPath); err == nil {
		t.Fatal("expected invalid path error")
	}
}

func TestSearchPrefixMatching(t *testing.T) {
	prefixes := normalizeSearchPrefixes(nil)
	if !searchPrefixMatches(prefixes, "anything") {
		t.Fatal("default empty prefix should match all keys")
	}

	prefixes = normalizeSearchPrefixes([]string{"user:", "product:"})
	if !searchPrefixMatches(prefixes, "product:1") {
		t.Fatal("product prefix did not match")
	}
	if searchPrefixMatches(prefixes, "order:1") {
		t.Fatal("unconfigured prefix matched")
	}
}

func TestSearchDocumentExtraction(t *testing.T) {
	state, err := extractSearchDocument(
		testSearchDefinition(),
		[]byte(`{
			"category":"books",
			"price":12.5,
			"tags":["go","db","go",{"ignored":true}],
			"variants":[{"price":10},{"price":14},{"price":10}]
		}`),
	)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := state.Tags["category"], []string{"books"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("category=%v want=%v", got, want)
	}
	if got, want := state.Tags["tags"], []string{"db", "go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tags=%v want=%v", got, want)
	}
	if got, want := state.Numerics["price"], []float64{12.5}; !reflect.DeepEqual(got, want) {
		t.Fatalf("price=%v want=%v", got, want)
	}
	if got, want := state.Numerics["variant_price"], []float64{10, 14}; !reflect.DeepEqual(got, want) {
		t.Fatalf("variant_price=%v want=%v", got, want)
	}
}

func TestSearchManagerCreateListDrop(t *testing.T) {
	m := newSearchManager()
	if err := m.create(testSearchDefinition()); err != nil {
		t.Fatal(err)
	}

	if err := m.create(testSearchDefinition()); err == nil {
		t.Fatal("duplicate index unexpectedly succeeded")
	}

	if got, want := m.names(), []string{"products"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("names=%v want=%v", got, want)
	}

	if !m.drop("products") {
		t.Fatal("drop returned false")
	}
	if m.drop("products") {
		t.Fatal("second drop unexpectedly succeeded")
	}
}

func TestSearchManagerTagAndNumericQueries(t *testing.T) {
	m := newSearchManager()
	if err := m.create(testSearchDefinition()); err != nil {
		t.Fatal(err)
	}

	docs := map[string]string{
		"product:3": `{"category":"books","price":30,"tags":["systems"],"variants":[{"price":31}]}`,
		"product:1": `{"category":"books","price":10,"tags":["go","db"],"variants":[{"price":8},{"price":12}]}`,
		"product:2": `{"category":"games","price":20,"tags":["go"],"variants":[{"price":18},{"price":25}]}`,
		"ignored:1": `{"category":"books","price":15,"tags":["go"]}`,
	}
	for key, raw := range docs {
		if err := m.replaceJSON(key, []byte(raw)); err != nil {
			t.Fatal(err)
		}
	}

	if got, want := mustTagKeys(t, m, "products", "category", "books"), []string{"product:1", "product:3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tag keys=%v want=%v", got, want)
	}

	if got, want := mustNumericKeys(t, m, "products", "price", 10, 20), []string{"product:1", "product:2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("numeric keys=%v want=%v", got, want)
	}

	if got, want := mustNumericKeys(t, m, "products", "variant_price", 11, 24), []string{"product:1", "product:2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("multi-value numeric keys=%v want=%v", got, want)
	}
}

func TestSearchManagerReplacementRemovesOldPostings(t *testing.T) {
	m := newSearchManager()
	if err := m.create(testSearchDefinition()); err != nil {
		t.Fatal(err)
	}

	if err := m.replaceJSON("product:1", []byte(`{"category":"books","price":10}`)); err != nil {
		t.Fatal(err)
	}
	if err := m.replaceJSON("product:1", []byte(`{"category":"games","price":40}`)); err != nil {
		t.Fatal(err)
	}

	if got := mustTagKeys(t, m, "products", "category", "books"); len(got) != 0 {
		t.Fatalf("stale tag posting=%v", got)
	}
	if got, want := mustTagKeys(t, m, "products", "category", "games"), []string{"product:1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("new tag posting=%v want=%v", got, want)
	}
	if got := mustNumericKeys(t, m, "products", "price", 0, 20); len(got) != 0 {
		t.Fatalf("stale numeric posting=%v", got)
	}
	if got, want := mustNumericKeys(t, m, "products", "price", 40, 40), []string{"product:1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("new numeric posting=%v want=%v", got, want)
	}
}

func TestSearchManagerRemoveKey(t *testing.T) {
	m := newSearchManager()
	if err := m.create(testSearchDefinition()); err != nil {
		t.Fatal(err)
	}

	if err := m.replaceJSON("product:1", []byte(`{"category":"books","price":10}`)); err != nil {
		t.Fatal(err)
	}
	m.removeKey("product:1")

	if got := mustTagKeys(t, m, "products", "category", "books"); len(got) != 0 {
		t.Fatalf("tag posting remained=%v", got)
	}
	if got := mustNumericKeys(t, m, "products", "price", 0, 100); len(got) != 0 {
		t.Fatalf("numeric posting remained=%v", got)
	}
}

func TestSearchIntersectionDeterministic(t *testing.T) {
	got := intersectSearchKeys(
		[]string{"product:3", "product:1", "product:2"},
		[]string{"product:2", "product:1"},
		[]string{"product:1", "product:4"},
	)
	want := []string{"product:1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("intersection=%v want=%v", got, want)
	}
}

func TestSearchMemoryBytesNonZeroOnlyAfterIndexData(t *testing.T) {
	m := newSearchManager()
	if got := m.memoryBytes(); got != 0 {
		t.Fatalf("empty manager memory=%d want=0", got)
	}

	if err := m.create(testSearchDefinition()); err != nil {
		t.Fatal(err)
	}
	base := m.memoryBytes()
	if base == 0 {
		t.Fatal("index definition should account for memory")
	}

	if err := m.replaceJSON("product:1", []byte(`{"category":"books","price":10}`)); err != nil {
		t.Fatal(err)
	}
	if got := m.memoryBytes(); got <= base {
		t.Fatalf("indexed document memory=%d base=%d", got, base)
	}
}

func mustTagKeys(t *testing.T, m *searchManager, indexName, alias, value string) []string {
	t.Helper()
	keys, ok := m.tagKeys(indexName, alias, value)
	if !ok {
		t.Fatalf("missing index %q", indexName)
	}
	return keys
}

func mustNumericKeys(t *testing.T, m *searchManager, indexName, alias string, min, max float64) []string {
	t.Helper()
	keys, ok := m.numericRangeKeys(indexName, alias, min, max)
	if !ok {
		t.Fatalf("missing index %q", indexName)
	}
	return keys
}
