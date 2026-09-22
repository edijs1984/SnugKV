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

	want := "*3\r\n:1\r\n$9\r\nproduct:2\r\n*2\r\n$7\r\n$.title\r\n$3\r\n\"B\"\r\n"
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

	want := "*3\r\n:1\r\n$9\r\nproduct:2\r\n*2\r\n$5\r\ntitle\r\n$3\r\n\"B\"\r\n"
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
	if !strings.Contains(got, "$.title") || !strings.Contains(got, "\"B\"") {
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
