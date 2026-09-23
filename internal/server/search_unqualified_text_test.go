package server

import (
	"strings"
	"testing"
)

func setupUnqualifiedTextSearch(t *testing.T) *Server {
	t.Helper()
	s := newSearchTestServer(t)

	docs := []struct {
		key  string
		json string
	}{
		{"doc:1", `{"title":"memory guide","body":"fast storage engine","tag":"docs","price":10}`},
		{"doc:2", `{"title":"server design","body":"memory system guide","tag":"infra","price":20}`},
		{"doc:3", `{"title":"running system","body":"execution manual","tag":"docs","price":30}`},
		{"doc:4", `{"title":"memry handbook","body":"search memory","tag":"misc","price":40}`},
		{"doc:5", `{"title":"the memory guide","body":"the fast engine","tag":"docs","price":50}`},
	}
	for _, doc := range docs {
		if _, err := s.Execute([][]byte{
			[]byte("JSON.SET"), []byte(doc.key), []byte("$"), []byte(doc.json),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("unq"),
		[]byte("ON"), []byte("JSON"),
		[]byte("PREFIX"), []byte("1"), []byte("doc:"),
		[]byte("SCHEMA"),
		[]byte("$.title"), []byte("AS"), []byte("title"), []byte("TEXT"),
		[]byte("$.body"), []byte("AS"), []byte("body"), []byte("TEXT"),
		[]byte("$.tag"), []byte("AS"), []byte("tag"), []byte("TAG"),
		[]byte("$.price"), []byte("AS"), []byte("price"), []byte("NUMERIC"),
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func searchNoContentKeys(t *testing.T, s *Server, index, query string, extra ...string) []string {
	t.Helper()
	args := [][]byte{[]byte("FT.SEARCH"), []byte(index), []byte(query), []byte("NOCONTENT")}
	for _, arg := range extra {
		args = append(args, []byte(arg))
	}
	reply, err := s.Execute(args)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(reply)
	parts := strings.Split(raw, "\r\n")
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "*") || !strings.HasPrefix(parts[1], ":") {
		t.Fatalf("bad reply %q", raw)
	}
	keys := make([]string, 0)
	for i := 2; i+1 < len(parts); i += 2 {
		if strings.HasPrefix(parts[i], "$") {
			keys = append(keys, parts[i+1])
		}
	}
	return keys
}

func requireSearchKeys(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("keys=%v want=%v", got, want)
	}
}

func TestFTSearchUnqualifiedTextTerms(t *testing.T) {
	s := setupUnqualifiedTextSearch(t)

	requireSearchKeys(t, searchNoContentKeys(t, s, "unq", "memory"),
		"doc:1", "doc:2", "doc:4", "doc:5")
	requireSearchKeys(t, searchNoContentKeys(t, s, "unq", "memory guide"),
		"doc:1", "doc:2", "doc:5")
	requireSearchKeys(t, searchNoContentKeys(t, s, "unq", "memory engine"),
		"doc:1", "doc:5")
	requireSearchKeys(t, searchNoContentKeys(t, s, "unq", "memory system guide"),
		"doc:2")
}

func TestFTSearchUnqualifiedTextMixedFielded(t *testing.T) {
	s := setupUnqualifiedTextSearch(t)

	requireSearchKeys(t, searchNoContentKeys(t, s, "unq", "@title:memory guide"),
		"doc:1", "doc:5")
	requireSearchKeys(t, searchNoContentKeys(t, s, "unq", "memory @body:guide"),
		"doc:2")
	requireSearchKeys(t, searchNoContentKeys(t, s, "unq", "@tag:{docs} memory"),
		"doc:1", "doc:5")
	requireSearchKeys(t, searchNoContentKeys(t, s, "unq", "memory @price:[10 30]"),
		"doc:1", "doc:2")
}

func TestFTSearchUnqualifiedPrefixFuzzyAndPhrase(t *testing.T) {
	s := setupUnqualifiedTextSearch(t)

	requireSearchKeys(t, searchNoContentKeys(t, s, "unq", "mem*"),
		"doc:1", "doc:2", "doc:4", "doc:5")
	requireSearchKeys(t, searchNoContentKeys(t, s, "unq", "%memory%"),
		"doc:1", "doc:2", "doc:4", "doc:5")
	requireSearchKeys(t, searchNoContentKeys(t, s, "unq", "%memry%"),
		"doc:1", "doc:2", "doc:4", "doc:5")
	requireSearchKeys(t, searchNoContentKeys(t, s, "unq", "\"memory guide\""),
		"doc:1", "doc:5")
	requireSearchKeys(t, searchNoContentKeys(t, s, "unq", "\"guide memory\""))
	requireSearchKeys(t, searchNoContentKeys(t, s, "unq", "\"memory system\""),
		"doc:2")
}

func TestFTSearchUnqualifiedStopwordsAndNoStem(t *testing.T) {
	s := setupUnqualifiedTextSearch(t)

	requireSearchKeys(t, searchNoContentKeys(t, s, "unq", "the"))
	requireSearchKeys(t, searchNoContentKeys(t, s, "unq", "the memory"),
		"doc:1", "doc:2", "doc:4", "doc:5")

	_, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"), []byte("unq"), []byte("\"the memory guide\""), []byte("NOCONTENT"),
	})
	if err == nil || err.Error() != "SEARCH_SYNTAX Syntax error at offset 1 near the" {
		t.Fatalf("phrase stopword err=%v", err)
	}

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("nostem"),
		[]byte("ON"), []byte("JSON"), []byte("PREFIX"), []byte("1"), []byte("doc:"),
		[]byte("SCHEMA"),
		[]byte("$.title"), []byte("AS"), []byte("title"), []byte("TEXT"), []byte("NOSTEM"),
		[]byte("$.body"), []byte("AS"), []byte("body"), []byte("TEXT"), []byte("NOSTEM"),
	}); err != nil {
		t.Fatal(err)
	}
	requireSearchKeys(t, searchNoContentKeys(t, s, "nostem", "run"))
	requireSearchKeys(t, searchNoContentKeys(t, s, "nostem", "running"), "doc:3")
}

func TestFTSearchUnqualifiedNoTextAndBoundaries(t *testing.T) {
	s := setupUnqualifiedTextSearch(t)

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("notext"),
		[]byte("ON"), []byte("JSON"), []byte("PREFIX"), []byte("1"), []byte("doc:"),
		[]byte("SCHEMA"),
		[]byte("$.tag"), []byte("AS"), []byte("tag"), []byte("TAG"),
		[]byte("$.price"), []byte("AS"), []byte("price"), []byte("NUMERIC"),
	}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"memory", "mem*", "%memory%", "\"memory guide\""} {
		requireSearchKeys(t, searchNoContentKeys(t, s, "notext", query))
	}

	requireSearchKeys(t, searchNoContentKeys(t, s, "unq", ""))
	requireSearchKeys(t, searchNoContentKeys(t, s, "unq", "*mem"))
	requireSearchKeys(t, searchNoContentKeys(t, s, "unq", "me*m"))

	for _, tc := range []struct {
		query string
		want  string
	}{
		{"%", "SEARCH_SYNTAX Syntax error at offset 0 near "},
		{"%%", "SEARCH_SYNTAX Syntax error at offset 1 near "},
		{"%%%%memory%%%%", "SEARCH_SYNTAX Syntax error at offset 3 near "},
	} {
		_, err := s.Execute([][]byte{
			[]byte("FT.SEARCH"), []byte("unq"), []byte(tc.query), []byte("NOCONTENT"),
		})
		if err == nil || err.Error() != tc.want {
			t.Fatalf("query=%q err=%v want=%q", tc.query, err, tc.want)
		}
	}
}

func TestFTSearchUnqualifiedDialectParity(t *testing.T) {
	s := setupUnqualifiedTextSearch(t)

	for _, query := range []string{"memory guide", "@title:memory guide", "\"memory guide\""} {
		d1 := searchNoContentKeys(t, s, "unq", query, "DIALECT", "1")
		d2 := searchNoContentKeys(t, s, "unq", query, "DIALECT", "2")
		if strings.Join(d1, ",") != strings.Join(d2, ",") {
			t.Fatalf("query=%q dialect1=%v dialect2=%v", query, d1, d2)
		}
	}
}
