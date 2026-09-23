package server

import "testing"

func setupWildcardSearch(t *testing.T) *Server {
	t.Helper()
	s := newSearchTestServer(t)
	docs := []struct {
		key, value string
	}{
		{"doc:1", `{"text":"memory guide"}`},
		{"doc:2", `{"text":"in-memory engine"}`},
		{"doc:3", `{"text":"memories manual"}`},
		{"doc:4", `{"text":"remember memory"}`},
		{"doc:5", `{"text":"primary storage"}`},
		{"doc:6", `{"text":"memXory token"}`},
		{"doc:7", `{"text":"running runner run"}`},
	}
	for _, doc := range docs {
		if _, err := s.Execute([][]byte{
			[]byte("JSON.SET"), []byte(doc.key), []byte("$"), []byte(doc.value),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("wc"),
		[]byte("ON"), []byte("JSON"),
		[]byte("PREFIX"), []byte("1"), []byte("doc:"),
		[]byte("SCHEMA"), []byte("$.text"), []byte("AS"), []byte("text"), []byte("TEXT"),
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFTSearchWildcardSuffixAndContains(t *testing.T) {
	s := setupWildcardSearch(t)

	requireSearchKeys(t, searchNoContentKeys(t, s, "wc", "@text:*ory"),
		"doc:1", "doc:2", "doc:4", "doc:6")
	requireSearchKeys(t, searchNoContentKeys(t, s, "wc", "@text:*mor*"),
		"doc:1", "doc:2", "doc:3", "doc:4")
	requireSearchKeys(t, searchNoContentKeys(t, s, "wc", "*ory"),
		"doc:1", "doc:2", "doc:4", "doc:6")
	requireSearchKeys(t, searchNoContentKeys(t, s, "wc", "*mor*"),
		"doc:1", "doc:2", "doc:3", "doc:4")
}

func TestFTSearchWildcardInternalFormsDoNotGlob(t *testing.T) {
	s := setupWildcardSearch(t)

	for _, query := range []string{
		"@text:m*mory",
		"@text:me*or*",
		"m*mory",
		"me*or*",
		"@text:m**y",
		"m**y",
		"@text:*m*e*m*",
		"*m*e*m*",
	} {
		requireSearchKeys(t, searchNoContentKeys(t, s, "wc", query))
	}
}

func TestFTSearchWildcardGroups(t *testing.T) {
	s := setupWildcardSearch(t)

	requireSearchKeys(t, searchNoContentKeys(t, s, "wc", "@text:(mem* guide)"), "doc:1")
	requireSearchKeys(t, searchNoContentKeys(t, s, "wc", "@text:(*ory guide)"), "doc:1")
	requireSearchKeys(t, searchNoContentKeys(t, s, "wc", "@text:(*mor* guide)"), "doc:1")
	requireSearchKeys(t, searchNoContentKeys(t, s, "wc", "@text:(mem* *ide)"), "doc:1")
}

func TestFTSearchWildcardPhraseErrors(t *testing.T) {
	s := setupWildcardSearch(t)

	for _, tc := range []struct {
		query, want string
	}{
		{`@text:"mem* guide"`, "SEARCH_SYNTAX Syntax error at offset 7 near mem"},
		{`"mem* guide"`, "SEARCH_SYNTAX Syntax error at offset 1 near mem"},
		{`@text:"*ory token"`, "SEARCH_SYNTAX Syntax error at offset 7 near ory"},
		{`"*ory token"`, "SEARCH_SYNTAX Syntax error at offset 1 near ory"},
	} {
		_, err := s.Execute([][]byte{
			[]byte("FT.SEARCH"), []byte("wc"), []byte(tc.query), []byte("NOCONTENT"),
		})
		if err == nil || err.Error() != tc.want {
			t.Fatalf("query=%q err=%v want=%q", tc.query, err, tc.want)
		}
	}
}

func TestFTSearchWildcardParserBoundaries(t *testing.T) {
	s := setupWildcardSearch(t)

	for _, tc := range []struct {
		query, want string
	}{
		{"@text:*", "SEARCH_SYNTAX Syntax error at offset 6 near text"},
		{"@text:**", "SEARCH_SYNTAX Syntax error at offset 6 near text"},
		{"@text:***", "SEARCH_SYNTAX Syntax error at offset 6 near text"},
		{"**", "SEARCH_SYNTAX Syntax error at offset 1 near "},
		{"***", "SEARCH_SYNTAX Syntax error at offset 1 near "},
		{"**memory**", "SEARCH_SYNTAX Syntax error at offset 1 near memory"},
	} {
		_, err := s.Execute([][]byte{
			[]byte("FT.SEARCH"), []byte("wc"), []byte(tc.query), []byte("NOCONTENT"),
		})
		if err == nil || err.Error() != tc.want {
			t.Fatalf("query=%q err=%v want=%q", tc.query, err, tc.want)
		}
	}
}

func TestFTSearchWildcardDialectParity(t *testing.T) {
	s := setupWildcardSearch(t)

	for _, query := range []string{"@text:*ory", "@text:*mor*", "@text:m*mory", "*ory", "*mor*", "m*mory"} {
		d1 := searchNoContentKeys(t, s, "wc", query, "DIALECT", "1")
		d2 := searchNoContentKeys(t, s, "wc", query, "DIALECT", "2")
		requireSearchKeys(t, d2, d1...)
	}
}


func TestFTSearchWildcardEscaping(t *testing.T) {
	s := setupWildcardSearch(t)

	requireSearchKeys(t, searchNoContentKeys(t, s, "wc", `@text:mem\*`))
	requireSearchKeys(t, searchNoContentKeys(t, s, "wc", `mem\*`))
	requireSearchKeys(t, searchNoContentKeys(t, s, "wc", `@text:\*ory`))
	requireSearchKeys(t, searchNoContentKeys(t, s, "wc", `\*ory`))

	requireSearchKeys(t, searchNoContentKeys(t, s, "wc", `@text:mem\\*`),
		"doc:1", "doc:2", "doc:3", "doc:4", "doc:6")
	requireSearchKeys(t, searchNoContentKeys(t, s, "wc", `mem\\*`),
		"doc:1", "doc:2", "doc:3", "doc:4", "doc:6")
}

func TestFTSearchWildcardFuzzyErrors(t *testing.T) {
	s := setupWildcardSearch(t)

	for _, tc := range []struct {
		query, want string
	}{
		{`@text:%mem*%`, "SEARCH_SYNTAX Syntax error at offset 7 near mem"},
		{`%mem*%`, "SEARCH_SYNTAX Syntax error at offset 1 near mem"},
		{`@text:*%memory%*`, "SEARCH_SYNTAX Syntax error at offset 6 near text"},
		{`*%memory%*`, "SEARCH_SYNTAX Syntax error at offset 1 near "},
	} {
		_, err := s.Execute([][]byte{
			[]byte("FT.SEARCH"), []byte("wc"), []byte(tc.query), []byte("NOCONTENT"),
		})
		if err == nil || err.Error() != tc.want {
			t.Fatalf("query=%q err=%v want=%q", tc.query, err, tc.want)
		}
	}
}
