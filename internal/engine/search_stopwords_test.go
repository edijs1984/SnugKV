package engine

import "testing"

func TestSearchStopwordsModes(t *testing.T) {
	defaultDef := SearchDefinition{}
	for _, word := range []string{"a", "the", "and", "with"} {
		if !SearchIsStopword(defaultDef, word) {
			t.Fatalf("%q should be a default stopword", word)
		}
	}
	if SearchIsStopword(defaultDef, "memory") {
		t.Fatal("memory unexpectedly treated as default stopword")
	}

	disabled := SearchDefinition{StopwordsConfigured: true}
	if SearchIsStopword(disabled, "the") {
		t.Fatal("STOPWORDS 0 should disable default stopwords")
	}

	custom := SearchDefinition{
		StopwordsConfigured: true,
		Stopwords:           []string{"foo", "BAR"},
	}
	normalizeSearchStopwords(&custom)
	if !SearchIsStopword(custom, "foo") || !SearchIsStopword(custom, "bar") {
		t.Fatal("custom stopwords not applied case-insensitively")
	}
	if SearchIsStopword(custom, "the") {
		t.Fatal("custom stopwords should replace, not extend, defaults")
	}
}
