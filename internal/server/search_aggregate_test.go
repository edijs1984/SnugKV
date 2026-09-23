package server

import (
	"strings"
	"testing"
)

func setupAggregateSearch(t *testing.T) *Server {
	t.Helper()
	s := newSearchTestServer(t)

	docs := []struct {
		key   string
		value string
	}{
		{"doc:1", `{"title":"memory guide","category":"db","price":10,"rank":30,"active":true}`},
		{"doc:2", `{"title":"memory engine","category":"db","price":20,"rank":10,"active":true}`},
		{"doc:3", `{"title":"server guide","category":"infra","price":15,"rank":20,"active":false}`},
		{"doc:4", `{"title":"memory server","category":"infra","price":25,"rank":40,"active":true}`},
		{"doc:5", `{"title":"cache guide","category":"db","price":30,"rank":50,"active":false}`},
	}
	for _, doc := range docs {
		if _, err := s.Execute([][]byte{
			[]byte("JSON.SET"), []byte(doc.key), []byte("$"), []byte(doc.value),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("agg"),
		[]byte("ON"), []byte("JSON"),
		[]byte("PREFIX"), []byte("1"), []byte("doc:"),
		[]byte("SCHEMA"),
		[]byte("$.title"), []byte("AS"), []byte("title"), []byte("TEXT"),
		[]byte("$.category"), []byte("AS"), []byte("category"), []byte("TAG"), []byte("SORTABLE"),
		[]byte("$.price"), []byte("AS"), []byte("price"), []byte("NUMERIC"), []byte("SORTABLE"),
		[]byte("$.rank"), []byte("AS"), []byte("rank"), []byte("NUMERIC"), []byte("SORTABLE"),
		[]byte("$.active"), []byte("AS"), []byte("active"), []byte("TAG"),
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFTAggregateLoadAndQuery(t *testing.T) {
	s := setupAggregateSearch(t)

	reply, err := s.Execute([][]byte{
		[]byte("FT.AGGREGATE"), []byte("agg"), []byte("memory"),
		[]byte("LOAD"), []byte("2"), []byte("@title"), []byte("@price"),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(reply)
	for _, want := range []string{"memory guide", "memory engine", "memory server", "price"} {
		if !strings.Contains(got, want) {
			t.Fatalf("reply missing %q: %q", want, reply)
		}
	}
	if strings.Contains(got, "cache guide") || strings.Contains(got, "server guide") {
		t.Fatalf("query leaked non-matching rows: %q", reply)
	}
}

func TestFTAggregateFilter(t *testing.T) {
	s := setupAggregateSearch(t)

	reply, err := s.Execute([][]byte{
		[]byte("FT.AGGREGATE"), []byte("agg"), []byte("*"),
		[]byte("LOAD"), []byte("2"), []byte("@title"), []byte("@price"),
		[]byte("FILTER"), []byte("@price > 15"),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(reply)
	for _, want := range []string{"memory engine", "memory server", "cache guide"} {
		if !strings.Contains(got, want) {
			t.Fatalf("filtered reply missing %q: %q", want, reply)
		}
	}
	if strings.Contains(got, "memory guide") || strings.Contains(got, "server guide") {
		t.Fatalf("filter retained low price rows: %q", reply)
	}

	_, err = s.Execute([][]byte{
		[]byte("FT.AGGREGATE"), []byte("agg"), []byte("*"),
		[]byte("FILTER"), []byte("@missing > 1"),
	})
	if err == nil || err.Error() != "SEARCH_PROP_NOT_FOUND Property not loaded nor in pipeline: `missing`" {
		t.Fatalf("missing property err=%v", err)
	}
}

func TestFTAggregateGroupReducers(t *testing.T) {
	s := setupAggregateSearch(t)

	cases := []struct {
		reducer string
		alias   string
		wants   []string
	}{
		{"COUNT", "count", []string{"count", "3", "2"}},
		{"SUM", "total", []string{"total", "60", "40"}},
		{"MIN", "min", []string{"min", "10", "15"}},
		{"MAX", "max", []string{"max", "30", "25"}},
		{"AVG", "avg", []string{"avg", "20"}},
	}
	for _, tc := range cases {
		args := [][]byte{
			[]byte("FT.AGGREGATE"), []byte("agg"), []byte("*"),
			[]byte("GROUPBY"), []byte("1"), []byte("@category"),
			[]byte("REDUCE"), []byte(tc.reducer),
		}
		if tc.reducer == "COUNT" {
			args = append(args, []byte("0"))
		} else {
			args = append(args, []byte("1"), []byte("@price"))
		}
		args = append(args, []byte("AS"), []byte(tc.alias))

		reply, err := s.Execute(args)
		if err != nil {
			t.Fatalf("%s: %v", tc.reducer, err)
		}
		got := string(reply)
		for _, want := range append([]string{"db", "infra"}, tc.wants...) {
			if !strings.Contains(got, want) {
				t.Fatalf("%s reply missing %q: %q", tc.reducer, want, reply)
			}
		}
	}
}

func TestFTAggregateSortAndLimit(t *testing.T) {
	s := setupAggregateSearch(t)

	reply, err := s.Execute([][]byte{
		[]byte("FT.AGGREGATE"), []byte("agg"), []byte("*"),
		[]byte("LOAD"), []byte("2"), []byte("@title"), []byte("@rank"),
		[]byte("SORTBY"), []byte("2"), []byte("@rank"), []byte("ASC"),
		[]byte("LIMIT"), []byte("1"), []byte("2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(reply)
	serverPos := strings.Index(got, "server guide")
	memoryPos := strings.Index(got, "memory guide")
	if serverPos < 0 || memoryPos < 0 || serverPos > memoryPos {
		t.Fatalf("unexpected sorted/limited reply: %q", reply)
	}
	if strings.Contains(got, "memory engine") || strings.Contains(got, "cache guide") {
		t.Fatalf("LIMIT leaked rows: %q", reply)
	}
}

func TestFTAggregateErrors(t *testing.T) {
	s := setupAggregateSearch(t)

	tests := []struct {
		args [][]byte
		want string
	}{
		{[][]byte{[]byte("FT.AGGREGATE"), []byte("missing"), []byte("*")}, "SEARCH_INDEX_NOT_FOUND Index not found: missing"},
		{[][]byte{[]byte("FT.AGGREGATE"), []byte("agg"), []byte("*"), []byte("LOAD")}, "SEARCH_PARSE_ARGS Bad arguments for LOAD: Expected an argument, but none provided"},
		{[][]byte{[]byte("FT.AGGREGATE"), []byte("agg"), []byte("*"), []byte("LIMIT"), []byte("-1"), []byte("2")}, "SEARCH_PARSE_ARGS LIMIT needs two numeric arguments"},
		{[][]byte{[]byte("FT.AGGREGATE"), []byte("agg"), []byte("*"), []byte("FILTER"), []byte("garbage")}, "SEARCH_EXPR Unknown symbol 'garbage'"},
	}
	for _, tc := range tests {
		_, err := s.Execute(tc.args)
		if err == nil || err.Error() != tc.want {
			t.Fatalf("args=%q err=%v want=%q", tc.args, err, tc.want)
		}
	}
}
