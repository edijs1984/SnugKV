package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func newSearchTestServer(t *testing.T) *Server {
	t.Helper()
	store, err := engine.NewWithShards(4)
	if err != nil {
		t.Fatal(err)
	}
	return New(store)
}

func TestFTCreateListDrop(t *testing.T) {
	s := newSearchTestServer(t)

	reply, err := s.Execute([][]byte{
		[]byte("FT.CREATE"),
		[]byte("products"),
		[]byte("ON"),
		[]byte("JSON"),
		[]byte("PREFIX"),
		[]byte("1"),
		[]byte("product:"),
		[]byte("SCHEMA"),
		[]byte("$.category"),
		[]byte("AS"),
		[]byte("category"),
		[]byte("TAG"),
		[]byte("$.price"),
		[]byte("AS"),
		[]byte("price"),
		[]byte("NUMERIC"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != "+OK\r\n" {
		t.Fatalf("reply=%q", reply)
	}

	reply, err = s.Execute([][]byte{
		[]byte("FT._LIST"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reply), "products") {
		t.Fatalf("FT._LIST=%q", reply)
	}

	reply, err = s.Execute([][]byte{
		[]byte("FT.DROPINDEX"),
		[]byte("products"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != "+OK\r\n" {
		t.Fatalf("drop reply=%q", reply)
	}

	reply, err = s.Execute([][]byte{
		[]byte("FT._LIST"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != "*0\r\n" {
		t.Fatalf("FT._LIST after drop=%q", reply)
	}
}

func TestFTCreateBackfillsExistingJSON(t *testing.T) {
	s := newSearchTestServer(t)

	if _, err := s.Execute([][]byte{
		[]byte("JSON.SET"),
		[]byte("product:1"),
		[]byte("$"),
		[]byte(`{"category":"books","price":12}`),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"),
		[]byte("products"),
		[]byte("ON"),
		[]byte("JSON"),
		[]byte("PREFIX"),
		[]byte("1"),
		[]byte("product:"),
		[]byte("SCHEMA"),
		[]byte("$.category"),
		[]byte("AS"),
		[]byte("category"),
		[]byte("TAG"),
	}); err != nil {
		t.Fatal(err)
	}

	keys, ok := s.store.SearchTagKeys("products", "category", "books")
	if !ok || len(keys) != 1 || keys[0] != "product:1" {
		t.Fatalf("backfill keys=%v ok=%v", keys, ok)
	}
}

func TestFTCreateValidation(t *testing.T) {
	s := newSearchTestServer(t)

	tests := []struct {
		name string
		args [][]byte
	}{
		{
			name: "requires ON JSON",
			args: [][]byte{
				[]byte("FT.CREATE"), []byte("idx"),
				[]byte("SCHEMA"),
				[]byte("$.x"), []byte("AS"), []byte("x"), []byte("TAG"),
			},
		},
		{
			name: "rejects unsupported ON type",
			args: [][]byte{
				[]byte("FT.CREATE"), []byte("idx"),
				[]byte("ON"), []byte("HASH"),
				[]byte("SCHEMA"),
				[]byte("$.x"), []byte("AS"), []byte("x"), []byte("TAG"),
			},
		},
		{
			name: "requires AS",
			args: [][]byte{
				[]byte("FT.CREATE"), []byte("idx"),
				[]byte("ON"), []byte("JSON"),
				[]byte("SCHEMA"),
				[]byte("$.x"), []byte("x"), []byte("TAG"),
			},
		},
		{
			name: "rejects unsupported field",
			args: [][]byte{
				[]byte("FT.CREATE"), []byte("idx"),
				[]byte("ON"), []byte("JSON"),
				[]byte("SCHEMA"),
				[]byte("$.x"), []byte("AS"), []byte("x"), []byte("TEXT"),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Execute(tc.args); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestFTCreateRejectsDuplicateIndexAndAlias(t *testing.T) {
	s := newSearchTestServer(t)

	create := [][]byte{
		[]byte("FT.CREATE"), []byte("products"),
		[]byte("ON"), []byte("JSON"),
		[]byte("SCHEMA"),
		[]byte("$.category"), []byte("AS"), []byte("category"), []byte("TAG"),
	}

	if _, err := s.Execute(create); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(create); err == nil {
		t.Fatal("duplicate index unexpectedly succeeded")
	}

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("bad"),
		[]byte("ON"), []byte("JSON"),
		[]byte("SCHEMA"),
		[]byte("$.a"), []byte("AS"), []byte("same"), []byte("TAG"),
		[]byte("$.b"), []byte("AS"), []byte("same"), []byte("NUMERIC"),
	}); err == nil {
		t.Fatal("duplicate alias unexpectedly succeeded")
	} else if got, want := err.Error(), "SEARCH_QUERY_BAD Duplicate field in schema - same"; got != want {
		t.Fatalf("duplicate alias error=%q want=%q", got, want)
	}
}

func TestFTCommandsExposeNoRedisKeys(t *testing.T) {
	for _, args := range [][][]byte{
		{
			[]byte("FT.CREATE"), []byte("idx"),
			[]byte("ON"), []byte("JSON"),
			[]byte("SCHEMA"),
			[]byte("$.x"), []byte("AS"), []byte("x"), []byte("TAG"),
		},
		{
			[]byte("FT.DROPINDEX"), []byte("idx"),
		},
		{
			[]byte("FT._LIST"),
		},
	} {
		refs, err := commandKeys(args)
		if err != nil {
			t.Fatal(err)
		}
		if len(refs) != 0 {
			t.Fatalf("%s unexpectedly exposed keys: %#v", args[0], refs)
		}
	}
}

func TestACLSearchCategoryIncludesImplementedFTCommands(t *testing.T) {
	commands, ok := aclCommandsForCategory("search")
	if !ok {
		t.Fatal("search category missing")
	}

	have := make(map[string]bool, len(commands))
	for _, command := range commands {
		have[command] = true
	}

	for _, command := range []string{
		"ft.create",
		"ft.dropindex",
		"ft._list",
	} {
		if !have[command] {
			t.Errorf("%s missing from @search", command)
		}
	}
}


func createProductSearchFixture(t *testing.T, s *Server) {
	t.Helper()

	for key, raw := range map[string]string{
		"product:1": `{"category":"books","price":10,"title":"A"}`,
		"product:2": `{"category":"games","price":20,"title":"B"}`,
		"product:3": `{"category":"books","price":30,"title":"C"}`,
	} {
		if _, err := s.Execute([][]byte{
			[]byte("JSON.SET"),
			[]byte(key),
			[]byte("$"),
			[]byte(raw),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"),
		[]byte("products"),
		[]byte("ON"),
		[]byte("JSON"),
		[]byte("PREFIX"),
		[]byte("1"),
		[]byte("product:"),
		[]byte("SCHEMA"),
		[]byte("$.category"),
		[]byte("AS"),
		[]byte("category"),
		[]byte("TAG"),
		[]byte("$.price"),
		[]byte("AS"),
		[]byte("price"),
		[]byte("NUMERIC"),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFTSearchMatchAllNoContent(t *testing.T) {
	s := newSearchTestServer(t)
	createProductSearchFixture(t, s)

	reply, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"),
		[]byte("products"),
		[]byte("*"),
		[]byte("NOCONTENT"),
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "*4\r\n:3\r\n$9\r\nproduct:1\r\n$9\r\nproduct:2\r\n$9\r\nproduct:3\r\n"
	if string(reply) != want {
		t.Fatalf("reply=%q want=%q", reply, want)
	}
}

func TestFTSearchTagNumericAndImplicitAND(t *testing.T) {
	s := newSearchTestServer(t)
	createProductSearchFixture(t, s)

	tests := []struct {
		query string
		want  string
	}{
		{
			query: "@category:{books}",
			want:  "*3\r\n:2\r\n$9\r\nproduct:1\r\n$9\r\nproduct:3\r\n",
		},
		{
			query: "@price:[10 20]",
			want:  "*3\r\n:2\r\n$9\r\nproduct:1\r\n$9\r\nproduct:2\r\n",
		},
		{
			query: "@category:{books} @price:[20 40]",
			want:  "*2\r\n:1\r\n$9\r\nproduct:3\r\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			reply, err := s.Execute([][]byte{
				[]byte("FT.SEARCH"),
				[]byte("products"),
				[]byte(tc.query),
				[]byte("NOCONTENT"),
			})
			if err != nil {
				t.Fatal(err)
			}
			if string(reply) != tc.want {
				t.Fatalf("reply=%q want=%q", reply, tc.want)
			}
		})
	}
}

func TestFTSearchLimitPreservesTotal(t *testing.T) {
	s := newSearchTestServer(t)
	createProductSearchFixture(t, s)

	reply, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"),
		[]byte("products"),
		[]byte("*"),
		[]byte("NOCONTENT"),
		[]byte("LIMIT"),
		[]byte("1"),
		[]byte("1"),
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "*2\r\n:3\r\n$9\r\nproduct:2\r\n"
	if string(reply) != want {
		t.Fatalf("reply=%q want=%q", reply, want)
	}
}

func TestFTSearchReturnsJSONContent(t *testing.T) {
	s := newSearchTestServer(t)
	createProductSearchFixture(t, s)

	reply, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"),
		[]byte("products"),
		[]byte("@category:{games}"),
	})
	if err != nil {
		t.Fatal(err)
	}

	got := string(reply)
	if !strings.Contains(got, "product:2") {
		t.Fatalf("missing key in reply=%q", reply)
	}
	if !strings.Contains(got, "$") {
		t.Fatalf("missing root field in reply=%q", reply)
	}
	if !strings.Contains(got, `{"category":"games","price":20,"title":"B"}`) {
		t.Fatalf("missing JSON content in reply=%q", reply)
	}
}

func TestFTSearchRejectsUnsupportedGrammar(t *testing.T) {
	s := newSearchTestServer(t)
	createProductSearchFixture(t, s)

	for _, query := range []string{
		"books",
		"@price:10",
		"@price:[10]",
		"@category:(books)",
	} {
		if _, err := s.Execute([][]byte{
			[]byte("FT.SEARCH"),
			[]byte("products"),
			[]byte(query),
		}); err == nil {
			t.Fatalf("query %q unexpectedly succeeded", query)
		}
	}
}

func TestFTSearchUnknownIndex(t *testing.T) {
	s := newSearchTestServer(t)

	if _, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"),
		[]byte("missing"),
		[]byte("*"),
	}); err == nil {
		t.Fatal("unknown index unexpectedly succeeded")
	}
}

func TestACLSearchCategoryAndReadIncludeFTSearch(t *testing.T) {
	searchCommands, ok := aclCommandsForCategory("search")
	if !ok {
		t.Fatal("search category missing")
	}
	readCommands, ok := aclCommandsForCategory("read")
	if !ok {
		t.Fatal("read category missing")
	}

	contains := func(commands []string, target string) bool {
		for _, command := range commands {
			if command == target {
				return true
			}
		}
		return false
	}

	if !contains(searchCommands, "ft.search") {
		t.Fatal("ft.search missing from @search")
	}
	if !contains(readCommands, "ft.search") {
		t.Fatal("ft.search missing from @read")
	}
}


func TestFTDropIndexMissingMatchesRedisError(t *testing.T) {
	s := newSearchTestServer(t)

	_, err := s.Execute([][]byte{
		[]byte("FT.DROPINDEX"),
		[]byte("products"),
	})
	if err == nil {
		t.Fatal("missing index unexpectedly succeeded")
	}

	want := "SEARCH_INDEX_NOT_FOUND Index not found: products"
	if err.Error() != want {
		t.Fatalf("error=%q want=%q", err.Error(), want)
	}
}


func TestFTSearchReturnJSONPath(t *testing.T) {
	s := newSearchTestServer(t)
	createProductSearchFixture(t, s)

	reply, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"),
		[]byte("products"),
		[]byte("@category:{games}"),
		[]byte("RETURN"),
		[]byte("1"),
		[]byte("$.title"),
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "*3\r\n:1\r\n$9\r\nproduct:2\r\n*2\r\n$7\r\n$.title\r\n$1\r\nB\r\n"
	if string(reply) != want {
		t.Fatalf("reply=%q want=%q", reply, want)
	}
}

func TestFTSearchReturnJSONPathAsAlias(t *testing.T) {
	s := newSearchTestServer(t)
	createProductSearchFixture(t, s)

	reply, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"),
		[]byte("products"),
		[]byte("@category:{games}"),
		[]byte("RETURN"),
		[]byte("3"),
		[]byte("$.title"),
		[]byte("AS"),
		[]byte("title"),
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "*3\r\n:1\r\n$9\r\nproduct:2\r\n*2\r\n$5\r\ntitle\r\n$1\r\nB\r\n"
	if string(reply) != want {
		t.Fatalf("reply=%q want=%q", reply, want)
	}
}

func TestFTSearchReturnMultipleJSONPaths(t *testing.T) {
	s := newSearchTestServer(t)
	createProductSearchFixture(t, s)

	reply, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"),
		[]byte("products"),
		[]byte("@category:{games}"),
		[]byte("RETURN"),
		[]byte("4"),
		[]byte("$.title"),
		[]byte("$.price"),
		[]byte("AS"),
		[]byte("cost"),
	})
	if err != nil {
		t.Fatal(err)
	}

	got := string(reply)
	if !strings.Contains(got, "$.title") || !strings.Contains(got, "$1\r\nB\r\n") {
		t.Fatalf("missing title projection in reply=%q", reply)
	}
	if !strings.Contains(got, "cost") || !strings.Contains(got, "20") {
		t.Fatalf("missing price projection in reply=%q", reply)
	}
}

func TestFTSearchReturnZeroActsLikeNoContent(t *testing.T) {
	s := newSearchTestServer(t)
	createProductSearchFixture(t, s)

	reply, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"),
		[]byte("products"),
		[]byte("*"),
		[]byte("RETURN"),
		[]byte("0"),
		[]byte("LIMIT"),
		[]byte("0"),
		[]byte("1"),
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "*2\r\n:3\r\n$9\r\nproduct:1\r\n"
	if string(reply) != want {
		t.Fatalf("reply=%q want=%q", reply, want)
	}
}

func TestFTSearchReturnMissingPathIsOmitted(t *testing.T) {
	s := newSearchTestServer(t)
	createProductSearchFixture(t, s)

	reply, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"),
		[]byte("products"),
		[]byte("@category:{games}"),
		[]byte("RETURN"),
		[]byte("1"),
		[]byte("$.missing"),
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "*3\r\n:1\r\n$9\r\nproduct:2\r\n*0\r\n"
	if string(reply) != want {
		t.Fatalf("reply=%q want=%q", reply, want)
	}
}

func TestFTSearchReturnRejectsNonJSONPathForNow(t *testing.T) {
	s := newSearchTestServer(t)
	createProductSearchFixture(t, s)

	_, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"),
		[]byte("products"),
		[]byte("*"),
		[]byte("RETURN"),
		[]byte("1"),
		[]byte("title"),
	})
	if err == nil {
		t.Fatal("non-JSONPath RETURN unexpectedly succeeded")
	}
}


func TestFTSearchReturnScalarShapes(t *testing.T) {
	s := newSearchTestServer(t)

	if _, err := s.Execute([][]byte{
		[]byte("JSON.SET"),
		[]byte("product:1"),
		[]byte("$"),
		[]byte("{\"s\":\"A\",\"n\":10,\"b\":true,\"z\":null,\"o\":{\"x\":1},\"a\":[1,2]}"),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"),
		[]byte("products"),
		[]byte("ON"),
		[]byte("JSON"),
		[]byte("PREFIX"),
		[]byte("1"),
		[]byte("product:"),
		[]byte("SCHEMA"),
		[]byte("$.n"),
		[]byte("AS"),
		[]byte("n"),
		[]byte("NUMERIC"),
	}); err != nil {
		t.Fatal(err)
	}

	reply, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"),
		[]byte("products"),
		[]byte("*"),
		[]byte("RETURN"),
		[]byte("6"),
		[]byte("$.s"),
		[]byte("$.n"),
		[]byte("$.b"),
		[]byte("$.z"),
		[]byte("$.o"),
		[]byte("$.a"),
	})
	if err != nil {
		t.Fatal(err)
	}

	got := string(reply)
	for _, want := range []string{
		"$.s\r\n$1\r\nA\r\n",
		"$.n\r\n$2\r\n10\r\n",
		"$.b\r\n$4\r\ntrue\r\n",
		"$.z\r\n$4\r\nnull\r\n",
		"{\"x\":1}",
		"[1,2]",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("reply=%q missing %q", reply, want)
		}
	}
}


func TestFTInfoReportsJSONIndex(t *testing.T) {
	s := newSearchTestServer(t)

	if _, err := s.Execute([][]byte{
		[]byte("JSON.SET"),
		[]byte("product:1"),
		[]byte("$"),
		[]byte(`{"category":"books","price":12}`),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"),
		[]byte("products"),
		[]byte("ON"),
		[]byte("JSON"),
		[]byte("PREFIX"),
		[]byte("1"),
		[]byte("product:"),
		[]byte("SCHEMA"),
		[]byte("$.category"),
		[]byte("AS"),
		[]byte("category"),
		[]byte("TAG"),
		[]byte("$.price"),
		[]byte("AS"),
		[]byte("price"),
		[]byte("NUMERIC"),
	}); err != nil {
		t.Fatal(err)
	}

	reply, err := s.Execute([][]byte{
		[]byte("FT.INFO"),
		[]byte("products"),
	})
	if err != nil {
		t.Fatal(err)
	}

	got := string(reply)
	for _, want := range []string{
		"index_name",
		"products",
		"index_options",
		"index_definition",
		"key_type",
		"JSON",
		"prefixes",
		"product:",
		"default_score",
		"attributes",
		"identifier",
		"$.category",
		"attribute",
		"category",
		"TAG",
		"$.price",
		"price",
		"NUMERIC",
		"num_docs",
		"indexing",
		"percent_indexed",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("FT.INFO reply=%q missing %q", reply, want)
		}
	}
}

func TestFTInfoDocumentCountTracksMutations(t *testing.T) {
	s := newSearchTestServer(t)

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"),
		[]byte("products"),
		[]byte("ON"),
		[]byte("JSON"),
		[]byte("PREFIX"),
		[]byte("1"),
		[]byte("product:"),
		[]byte("SCHEMA"),
		[]byte("$.category"),
		[]byte("AS"),
		[]byte("category"),
		[]byte("TAG"),
	}); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"product:1", "product:2"} {
		if _, err := s.Execute([][]byte{
			[]byte("JSON.SET"),
			[]byte(key),
			[]byte("$"),
			[]byte(`{"category":"books"}`),
		}); err != nil {
			t.Fatal(err)
		}
	}

	reply, err := s.Execute([][]byte{
		[]byte("FT.INFO"),
		[]byte("products"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reply), "$8\r\nnum_docs\r\n:2\r\n") {
		t.Fatalf("FT.INFO before delete=%q", reply)
	}

	if _, err := s.Execute([][]byte{
		[]byte("DEL"),
		[]byte("product:2"),
	}); err != nil {
		t.Fatal(err)
	}

	reply, err = s.Execute([][]byte{
		[]byte("FT.INFO"),
		[]byte("products"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reply), "$8\r\nnum_docs\r\n:1\r\n") {
		t.Fatalf("FT.INFO after delete=%q", reply)
	}
}

func TestFTInfoMissingMatchesRedisSearchError(t *testing.T) {
	s := newSearchTestServer(t)

	_, err := s.Execute([][]byte{
		[]byte("FT.INFO"),
		[]byte("missing"),
	})
	if err == nil {
		t.Fatal("FT.INFO missing index unexpectedly succeeded")
	}
	if got, want := err.Error(), "SEARCH_INDEX_NOT_FOUND Index not found: missing"; got != want {
		t.Fatalf("error=%q want=%q", got, want)
	}
}


func TestFTSearchUnknownOrWrongTypeFieldReturnsZero(t *testing.T) {
	s := newSearchTestServer(t)
	createProductSearchFixture(t, s)

	for _, query := range []string{
		"@missing:{books}",
		"@price:{10}",
		"@category:[1 2]",
	} {
		reply, err := s.Execute([][]byte{
			[]byte("FT.SEARCH"),
			[]byte("products"),
			[]byte(query),
			[]byte("NOCONTENT"),
		})
		if err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		if got, want := string(reply), "*1\r\n:0\r\n"; got != want {
			t.Fatalf("query %q reply=%q want=%q", query, got, want)
		}
	}
}

func TestFTSearchRejectsNonFiniteNumericBounds(t *testing.T) {
	s := newSearchTestServer(t)
	createProductSearchFixture(t, s)

	tests := []struct {
		query string
		want  string
	}{
		{"@price:[NaN 20]", "SEARCH_SYNTAX Syntax error at offset 8 near NaN"},
		{"@price:[10 NaN]", "SEARCH_SYNTAX Syntax error at offset 11 near NaN"},
	}

	for _, tc := range tests {
		_, err := s.Execute([][]byte{
			[]byte("FT.SEARCH"),
			[]byte("products"),
			[]byte(tc.query),
		})
		if err == nil {
			t.Fatalf("query %q unexpectedly succeeded", tc.query)
		}
		if got := err.Error(); got != tc.want {
			t.Fatalf("query %q error=%q want=%q", tc.query, got, tc.want)
		}
	}
}

func TestFTSearchMissingIndexMatchesSearchError(t *testing.T) {
	s := newSearchTestServer(t)

	_, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"),
		[]byte("missing"),
		[]byte("*"),
	})
	if err == nil {
		t.Fatal("unknown index unexpectedly succeeded")
	}

	want := "SEARCH_INDEX_NOT_FOUND Index not found: missing"
	if err.Error() != want {
		t.Fatalf("error=%q want=%q", err.Error(), want)
	}
}

func TestFTCreateAllowsMalformedJSONPathDefinitionLikeRedis(t *testing.T) {
	s := newSearchTestServer(t)

	reply, err := s.Execute([][]byte{
		[]byte("FT.CREATE"),
		[]byte("badpath"),
		[]byte("ON"),
		[]byte("JSON"),
		[]byte("SCHEMA"),
		[]byte("$["),
		[]byte("AS"),
		[]byte("broken"),
		[]byte("TAG"),
	})
	if err != nil {
		t.Fatalf("FT.CREATE malformed path: %v", err)
	}
	if got, want := string(reply), "+OK\r\n"; got != want {
		t.Fatalf("reply=%q want=%q", got, want)
	}
}


func TestFTCreateAllowsMalformedJSONPathWithExistingDocuments(t *testing.T) {
	s := newSearchTestServer(t)

	if _, err := s.Execute([][]byte{
		[]byte("JSON.SET"),
		[]byte("product:1"),
		[]byte("$"),
		[]byte(`{"category":"books"}`),
	}); err != nil {
		t.Fatal(err)
	}

	reply, err := s.Execute([][]byte{
		[]byte("FT.CREATE"),
		[]byte("badpath"),
		[]byte("ON"),
		[]byte("JSON"),
		[]byte("SCHEMA"),
		[]byte("$["),
		[]byte("AS"),
		[]byte("broken"),
		[]byte("TAG"),
	})
	if err != nil {
		t.Fatalf("FT.CREATE malformed path with backfill: %v", err)
	}
	if got, want := string(reply), "+OK\r\n"; got != want {
		t.Fatalf("reply=%q want=%q", got, want)
	}

	reply, err = s.Execute([][]byte{
		[]byte("FT._LIST"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reply), "badpath") {
		t.Fatalf("FT._LIST=%q missing badpath index", reply)
	}
}


func TestFTSearchSortByNumeric(t *testing.T) {
	s := newSearchTestServer(t)
	createProductSearchFixture(t, s)

	tests := []struct {
		name string
		args [][]byte
		want string
	}{
		{
			name: "ascending default",
			args: [][]byte{
				[]byte("FT.SEARCH"), []byte("products"), []byte("*"),
				[]byte("NOCONTENT"), []byte("SORTBY"), []byte("price"),
			},
			want: "*4\r\n:3\r\n$9\r\nproduct:1\r\n$9\r\nproduct:2\r\n$9\r\nproduct:3\r\n",
		},
		{
			name: "descending",
			args: [][]byte{
				[]byte("FT.SEARCH"), []byte("products"), []byte("*"),
				[]byte("NOCONTENT"), []byte("SORTBY"), []byte("price"), []byte("DESC"),
			},
			want: "*4\r\n:3\r\n$9\r\nproduct:3\r\n$9\r\nproduct:2\r\n$9\r\nproduct:1\r\n",
		},
		{
			name: "sort before limit",
			args: [][]byte{
				[]byte("FT.SEARCH"), []byte("products"), []byte("*"),
				[]byte("NOCONTENT"), []byte("SORTBY"), []byte("price"), []byte("DESC"),
				[]byte("LIMIT"), []byte("1"), []byte("1"),
			},
			want: "*2\r\n:3\r\n$9\r\nproduct:2\r\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reply, err := s.Execute(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(reply); got != tc.want {
				t.Fatalf("reply=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestFTSearchSortByTag(t *testing.T) {
	s := newSearchTestServer(t)
	createProductSearchFixture(t, s)

	reply, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"), []byte("products"), []byte("*"),
		[]byte("NOCONTENT"),
		[]byte("SORTBY"), []byte("category"), []byte("ASC"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// books sorts before games; equal values use deterministic key order.
	want := "*4\r\n:3\r\n$9\r\nproduct:1\r\n$9\r\nproduct:3\r\n$9\r\nproduct:2\r\n"
	if got := string(reply); got != want {
		t.Fatalf("reply=%q want=%q", got, want)
	}
}

func TestFTSearchSortByUnknownField(t *testing.T) {
	s := newSearchTestServer(t)
	createProductSearchFixture(t, s)

	_, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"), []byte("products"), []byte("*"),
		[]byte("SORTBY"), []byte("missing"),
	})
	if err == nil {
		t.Fatal("unknown SORTBY field unexpectedly succeeded")
	}
	if got, want := err.Error(), "SEARCH_PROP_NOT_FOUND Property `missing` not loaded nor in schema"; got != want {
		t.Fatalf("error=%q want=%q", got, want)
	}
}

func TestFTSearchSortByRejectsDuplicateClause(t *testing.T) {
	s := newSearchTestServer(t)
	createProductSearchFixture(t, s)

	if _, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"), []byte("products"), []byte("*"),
		[]byte("SORTBY"), []byte("price"),
		[]byte("SORTBY"), []byte("category"),
	}); err == nil {
		t.Fatal("duplicate SORTBY unexpectedly succeeded")
	}
}


func TestFTSearchBooleanORAndNegation(t *testing.T) {
	s := newSearchTestServer(t)
	createProductSearchFixture(t, s)

	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "or tag numeric",
			query: "(@category:{games}) | (@price:[30 30])",
			want:  "*3\r\n:2\r\n$9\r\nproduct:2\r\n$9\r\nproduct:3\r\n",
		},
		{
			name:  "and with negation",
			query: "@category:{books} -@price:[30 30]",
			want:  "*2\r\n:1\r\n$9\r\nproduct:1\r\n",
		},
		{
			name:  "pure negation",
			query: "-@category:{games}",
			want:  "*3\r\n:2\r\n$9\r\nproduct:1\r\n$9\r\nproduct:3\r\n",
		},
		{
			name:  "or groups with implicit and",
			query: "(@category:{books} @price:[10 10]) | (@category:{games})",
			want:  "*3\r\n:2\r\n$9\r\nproduct:1\r\n$9\r\nproduct:2\r\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reply, err := s.Execute([][]byte{
				[]byte("FT.SEARCH"),
				[]byte("products"),
				[]byte(tc.query),
				[]byte("NOCONTENT"),
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := string(reply); got != tc.want {
				t.Fatalf("reply=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestFTSearchBooleanRejectsMalformedOperators(t *testing.T) {
	s := newSearchTestServer(t)
	createProductSearchFixture(t, s)

	for _, query := range []string{
		"|@category:{books}",
		"@category:{books}|",
		"@category:{books}||@category:{games}",
		"@category:{games}|@price:[30 30]",
		"@category:{games} | @price:[30 30]",
		"-",
	} {
		if _, err := s.Execute([][]byte{
			[]byte("FT.SEARCH"),
			[]byte("products"),
			[]byte(query),
		}); err == nil {
			t.Fatalf("query %q unexpectedly succeeded", query)
		}
	}
}


func TestFTSearchNestedBooleanExpressions(t *testing.T) {
	s := newSearchTestServer(t)
	createProductSearchFixture(t, s)

	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "nested or then and",
			query: "(@category:{books} | @category:{games}) @price:[20 30]",
			want:  "*3\r\n:2\r\n$9\r\nproduct:2\r\n$9\r\nproduct:3\r\n",
		},
		{
			name:  "or with nested and arm",
			query: "(@category:{games}) | (@category:{books} @price:[30 30])",
			want:  "*3\r\n:2\r\n$9\r\nproduct:2\r\n$9\r\nproduct:3\r\n",
		},
		{
			name:  "negated group",
			query: "-(@category:{games} | @price:[30 30])",
			want:  "*2\r\n:1\r\n$9\r\nproduct:1\r\n",
		},
		{
			name:  "nested grouping",
			query: "((@category:{books}) | (@category:{games})) @price:[10 20]",
			want:  "*3\r\n:2\r\n$9\r\nproduct:1\r\n$9\r\nproduct:2\r\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reply, err := s.Execute([][]byte{
				[]byte("FT.SEARCH"),
				[]byte("products"),
				[]byte(tc.query),
				[]byte("NOCONTENT"),
				[]byte("DIALECT"),
				[]byte("2"),
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := string(reply); got != tc.want {
				t.Fatalf("reply=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestFTSearchNestedBooleanRejectsUnbalancedParentheses(t *testing.T) {
	s := newSearchTestServer(t)
	createProductSearchFixture(t, s)

	for _, query := range []string{
		"(@category:{books}",
		"@category:{books})",
		"()",
		"(@category:{books} | )",
	} {
		if _, err := s.Execute([][]byte{
			[]byte("FT.SEARCH"),
			[]byte("products"),
			[]byte(query),
		}); err == nil {
			t.Fatalf("query %q unexpectedly succeeded", query)
		}
	}
}
