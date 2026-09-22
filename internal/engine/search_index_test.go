package engine

import (
	"reflect"
	"testing"
	"time"

	"snugkv/internal/persistence"
)

func testSearchDefinition() SearchDefinition {
	return SearchDefinition{
		Name:     "products",
		Prefixes: []string{"product:"},
		Fields: []SearchField{
			{Path: "$.category", Alias: "category", Kind: SearchFieldTag},
			{Path: "$.price", Alias: "price", Kind: SearchFieldNumeric},
			{Path: "$.tags[*]", Alias: "tags", Kind: SearchFieldTag},
			{Path: "$.variants[*].price", Alias: "variant_price", Kind: SearchFieldNumeric},
		},
	}
}

func TestSearchDefinitionValidation(t *testing.T) {
	if err := validateSearchDefinition(testSearchDefinition()); err != nil {
		t.Fatal(err)
	}

	duplicate := testSearchDefinition()
	duplicate.Fields = append(duplicate.Fields,
		SearchField{Path: "$.other", Alias: "price", Kind: SearchFieldTag},
	)
	if err := validateSearchDefinition(duplicate); err == nil {
		t.Fatal("expected duplicate alias error")
	}

	invalidPath := testSearchDefinition()
	invalidPath.Fields[0].Path = "$["
	if err := validateSearchDefinition(invalidPath); err != nil {
		t.Fatalf("malformed JSONPath definition should be accepted like Redis: %v", err)
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

func TestStoreSearchManagerIsLazy(t *testing.T) {
	store, err := NewWithShards(1)
	if err != nil {
		t.Fatal(err)
	}
	if store.getSearchManager() != nil {
		t.Fatal("search manager should be nil before first index")
	}
	if got := store.SearchMemoryBytes(); got != 0 {
		t.Fatalf("search memory=%d want=0", got)
	}

	if err := store.CreateSearchIndex(testSearchDefinition()); err != nil {
		t.Fatal(err)
	}
	if store.getSearchManager() == nil {
		t.Fatal("search manager was not initialized")
	}
	if got, want := store.SearchIndexNames(), []string{"products"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("names=%v want=%v", got, want)
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


func TestStoreSearchIndexBackfillsExistingJSON(t *testing.T) {
	store, err := NewWithShards(4)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.JSONSet(
		"product:1",
		"$",
		[]byte(`{"category":"books","price":10}`),
		false,
		false,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.JSONSet(
		"ignored:1",
		"$",
		[]byte(`{"category":"books","price":15}`),
		false,
		false,
	); err != nil {
		t.Fatal(err)
	}

	if err := store.CreateSearchIndex(testSearchDefinition()); err != nil {
		t.Fatal(err)
	}

	keys, ok := store.SearchTagKeys("products", "category", "books")
	if !ok {
		t.Fatal("index missing")
	}
	if got, want := keys, []string{"product:1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("backfill=%v want=%v", got, want)
	}
}

func TestStoreSearchIndexTracksJSONMutationAndDelete(t *testing.T) {
	store, err := NewWithShards(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSearchIndex(testSearchDefinition()); err != nil {
		t.Fatal(err)
	}

	if _, err := store.JSONSet(
		"product:1",
		"$",
		[]byte(`{"category":"books","price":10}`),
		false,
		false,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := store.JSONSet(
		"product:1",
		"$.category",
		[]byte(`"games"`),
		false,
		false,
	); err != nil {
		t.Fatal(err)
	}

	if got := mustStoreTagKeys(t, store, "products", "category", "books"); len(got) != 0 {
		t.Fatalf("stale category posting=%v", got)
	}
	if got, want := mustStoreTagKeys(t, store, "products", "category", "games"), []string{"product:1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("updated category posting=%v want=%v", got, want)
	}

	deleted, err := store.JSONDel("product:1", "$")
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted=%d want=1", deleted)
	}

	if got := mustStoreTagKeys(t, store, "products", "category", "games"); len(got) != 0 {
		t.Fatalf("posting remained after JSON.DEL=%v", got)
	}
}

func TestStoreSearchIndexRemovesJSONWhenOverwrittenByString(t *testing.T) {
	store, err := NewWithShards(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSearchIndex(testSearchDefinition()); err != nil {
		t.Fatal(err)
	}

	if _, err := store.JSONSet(
		"product:1",
		"$",
		[]byte(`{"category":"books","price":10}`),
		false,
		false,
	); err != nil {
		t.Fatal(err)
	}

	if err := store.Set("product:1", []byte("plain"), 0); err != nil {
		t.Fatal(err)
	}

	if got := mustStoreTagKeys(t, store, "products", "category", "books"); len(got) != 0 {
		t.Fatalf("posting remained after string overwrite=%v", got)
	}
}

func TestStoreSearchIndexTracksGenericDelete(t *testing.T) {
	store, err := NewWithShards(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSearchIndex(testSearchDefinition()); err != nil {
		t.Fatal(err)
	}

	if _, err := store.JSONSet(
		"product:1",
		"$",
		[]byte(`{"category":"books","price":10}`),
		false,
		false,
	); err != nil {
		t.Fatal(err)
	}
	if !store.Delete("product:1") {
		t.Fatal("delete returned false")
	}

	if got := mustStoreTagKeys(t, store, "products", "category", "books"); len(got) != 0 {
		t.Fatalf("posting remained after DEL=%v", got)
	}
}

func TestStoreSearchIndexTracksRestore(t *testing.T) {
	store, err := NewWithShards(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSearchIndex(testSearchDefinition()); err != nil {
		t.Fatal(err)
	}

	err = store.Restore([]persistence.Record{
		{
			Key:       []byte("product:1"),
			Value:     []byte(`{"category":"books","price":22}`),
			ValueType: uint8(TypeJSON),
		},
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := mustStoreTagKeys(t, store, "products", "category", "books"), []string{"product:1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("restore tag posting=%v want=%v", got, want)
	}
	if got, want := mustStoreNumericKeys(t, store, "products", "price", 22, 22), []string{"product:1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("restore numeric posting=%v want=%v", got, want)
	}
}

func TestStoreMemoryIncludesSearchBytes(t *testing.T) {
	store, err := NewWithShards(1)
	if err != nil {
		t.Fatal(err)
	}
	before := store.Memory()

	if err := store.CreateSearchIndex(testSearchDefinition()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.JSONSet(
		"product:1",
		"$",
		[]byte(`{"category":"books","price":10}`),
		false,
		false,
	); err != nil {
		t.Fatal(err)
	}

	after := store.Memory()
	if after.SearchBytes == 0 {
		t.Fatal("search bytes were not reported")
	}
	if after.AccountedBytes <= before.AccountedBytes {
		t.Fatalf("accounted bytes did not increase: before=%d after=%d", before.AccountedBytes, after.AccountedBytes)
	}
}

func mustStoreTagKeys(t *testing.T, store *Store, indexName, alias, value string) []string {
	t.Helper()
	keys, ok := store.SearchTagKeys(indexName, alias, value)
	if !ok {
		t.Fatalf("missing index %q", indexName)
	}
	return keys
}

func mustStoreNumericKeys(t *testing.T, store *Store, indexName, alias string, min, max float64) []string {
	t.Helper()
	keys, ok := store.SearchNumericRangeKeys(indexName, alias, min, max)
	if !ok {
		t.Fatalf("missing index %q", indexName)
	}
	return keys
}


func TestStoreSearchIndexDropsExpiredJSON(t *testing.T) {
	store, err := NewWithShards(1)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	store.now = func() time.Time { return now }

	if err := store.CreateSearchIndex(testSearchDefinition()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.JSONSet(
		"product:1",
		"$",
		[]byte(`{"category":"books","price":10}`),
		false,
		false,
	); err != nil {
		t.Fatal(err)
	}
	if !store.Expire("product:1", time.Second) {
		t.Fatal("expire returned false")
	}

	now = now.Add(2 * time.Second)
	if removed := store.CleanupExpiredLimit(10); removed != 1 {
		t.Fatalf("removed=%d want=1", removed)
	}

	if got := mustStoreTagKeys(t, store, "products", "category", "books"); len(got) != 0 {
		t.Fatalf("posting remained after expiration=%v", got)
	}
}

func TestStoreRenamePreservesJSONTypeAndSearchIndex(t *testing.T) {
	store, err := NewWithShards(4)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSearchIndex(testSearchDefinition()); err != nil {
		t.Fatal(err)
	}

	if _, err := store.JSONSet(
		"product:1",
		"$",
		[]byte(`{"category":"books","price":10}`),
		false,
		false,
	); err != nil {
		t.Fatal(err)
	}

	renamed, err := store.Rename("product:1", "product:2", false)
	if err != nil {
		t.Fatal(err)
	}
	if !renamed {
		t.Fatal("rename returned false")
	}

	if got := mustStoreTagKeys(t, store, "products", "category", "books"); !reflect.DeepEqual(got, []string{"product:2"}) {
		t.Fatalf("rename postings=%v want=[product:2]", got)
	}

	jsonType, found, err := store.JSONType("product:2", "$")
	if err != nil {
		t.Fatal(err)
	}
	if !found || jsonType != "object" {
		t.Fatalf("renamed JSON type=%q found=%v", jsonType, found)
	}
}

func TestStoreRenameJSONOutOfIndexedPrefixRemovesPosting(t *testing.T) {
	store, err := NewWithShards(4)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSearchIndex(testSearchDefinition()); err != nil {
		t.Fatal(err)
	}

	if _, err := store.JSONSet(
		"product:1",
		"$",
		[]byte(`{"category":"books","price":10}`),
		false,
		false,
	); err != nil {
		t.Fatal(err)
	}

	renamed, err := store.Rename("product:1", "archive:1", false)
	if err != nil {
		t.Fatal(err)
	}
	if !renamed {
		t.Fatal("rename returned false")
	}

	if got := mustStoreTagKeys(t, store, "products", "category", "books"); len(got) != 0 {
		t.Fatalf("posting remained after rename out of prefix=%v", got)
	}

	jsonType, found, err := store.JSONType("archive:1", "$")
	if err != nil {
		t.Fatal(err)
	}
	if !found || jsonType != "object" {
		t.Fatalf("renamed JSON type=%q found=%v", jsonType, found)
	}
}

func TestStoreCopyStyleRestoreIndexesDestination(t *testing.T) {
	store, err := NewWithShards(4)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSearchIndex(testSearchDefinition()); err != nil {
		t.Fatal(err)
	}

	if _, err := store.JSONSet(
		"product:1",
		"$",
		[]byte(`{"category":"books","price":10}`),
		false,
		false,
	); err != nil {
		t.Fatal(err)
	}

	records := store.Export([]string{"product:1"})
	if len(records) != 1 {
		t.Fatalf("records=%d want=1", len(records))
	}
	records[0].Key = []byte("product:2")

	if err := store.Restore(records, false); err != nil {
		t.Fatal(err)
	}

	if got, want := mustStoreTagKeys(t, store, "products", "category", "books"), []string{"product:1", "product:2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("copy-style postings=%v want=%v", got, want)
	}

	jsonType, found, err := store.JSONType("product:2", "$")
	if err != nil {
		t.Fatal(err)
	}
	if !found || jsonType != "object" {
		t.Fatalf("copied JSON type=%q found=%v", jsonType, found)
	}
}
