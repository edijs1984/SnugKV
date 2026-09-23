package server

import (
	"strings"
	"testing"
)

func setupPhoneticSearch(t *testing.T) *Server {
	t.Helper()
	s := newSearchTestServer(t)
	docs := []struct {
		key, value string
	}{
		{"doc:1", `{"name":"Jon Smith","body":"memory guide"}`},
		{"doc:2", `{"name":"John Smyth","body":"server design"}`},
		{"doc:3", `{"name":"Jan Schmidt","body":"memory system"}`},
		{"doc:4", `{"name":"Jane Smith","body":"storage engine"}`},
		{"doc:5", `{"name":"running","body":"execution manual"}`},
		{"doc:6", `{"name":"run","body":"runner system"}`},
	}
	for _, doc := range docs {
		if _, err := s.Execute([][]byte{
			[]byte("JSON.SET"), []byte(doc.key), []byte("$"), []byte(doc.value),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("ph"),
		[]byte("ON"), []byte("JSON"),
		[]byte("PREFIX"), []byte("1"), []byte("doc:"),
		[]byte("SCHEMA"),
		[]byte("$.name"), []byte("AS"), []byte("name"), []byte("TEXT"), []byte("PHONETIC"), []byte("dm:en"),
		[]byte("$.body"), []byte("AS"), []byte("body"), []byte("TEXT"),
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFTSearchPhoneticBasic(t *testing.T) {
	s := setupPhoneticSearch(t)

	for _, query := range []string{"@name:jon", "@name:john", "@name:jan", "@name:jane"} {
		requireSearchKeys(t, searchNoContentKeys(t, s, "ph", query),
			"doc:1", "doc:2", "doc:3", "doc:4")
	}
	for _, query := range []string{"@name:smith", "@name:smyth"} {
		requireSearchKeys(t, searchNoContentKeys(t, s, "ph", query),
			"doc:1", "doc:2", "doc:4")
	}
	requireSearchKeys(t, searchNoContentKeys(t, s, "ph", "@name:schmidt"), "doc:3")
}

func TestFTSearchPhoneticUnqualifiedAndComposition(t *testing.T) {
	s := setupPhoneticSearch(t)

	requireSearchKeys(t, searchNoContentKeys(t, s, "ph", "jon"),
		"doc:1", "doc:2", "doc:3", "doc:4")
	requireSearchKeys(t, searchNoContentKeys(t, s, "ph", "memory"),
		"doc:1", "doc:3")
	requireSearchKeys(t, searchNoContentKeys(t, s, "ph", "jon memory"),
		"doc:1", "doc:3")
	requireSearchKeys(t, searchNoContentKeys(t, s, "ph", "@name:(jon smith)"),
		"doc:1", "doc:2", "doc:4")
}

func TestFTSearchPhoneticDoesNotExpandPrefixOrPhrase(t *testing.T) {
	s := setupPhoneticSearch(t)

	requireSearchKeys(t, searchNoContentKeys(t, s, "ph", "@name:jo*"),
		"doc:1", "doc:2")
	requireSearchKeys(t, searchNoContentKeys(t, s, "ph", `@name:"jon smith"`),
		"doc:1")
}

func TestFTSearchPhoneticNostemIndependent(t *testing.T) {
	s := setupPhoneticSearch(t)

	requireSearchKeys(t, searchNoContentKeys(t, s, "ph", "@name:run"),
		"doc:5", "doc:6")
	requireSearchKeys(t, searchNoContentKeys(t, s, "ph", "@name:running"),
		"doc:5", "doc:6")

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("phnostem"),
		[]byte("ON"), []byte("JSON"), []byte("PREFIX"), []byte("1"), []byte("doc:"),
		[]byte("SCHEMA"), []byte("$.name"), []byte("AS"), []byte("name"),
		[]byte("TEXT"), []byte("NOSTEM"), []byte("PHONETIC"), []byte("dm:en"),
	}); err != nil {
		t.Fatal(err)
	}
	requireSearchKeys(t, searchNoContentKeys(t, s, "phnostem", "@name:run"), "doc:6")
	requireSearchKeys(t, searchNoContentKeys(t, s, "phnostem", "@name:running"), "doc:5")
}

func TestFTCreatePhoneticOrderingAndErrors(t *testing.T) {
	s := newSearchTestServer(t)

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("ok"),
		[]byte("ON"), []byte("JSON"), []byte("PREFIX"), []byte("1"), []byte("doc:"),
		[]byte("SCHEMA"), []byte("$.name"), []byte("AS"), []byte("name"),
		[]byte("TEXT"), []byte("PHONETIC"), []byte("dm:en"), []byte("SORTABLE"),
	}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		args [][]byte
		want string
	}{
		{
			"missing",
			[][]byte{[]byte("FT.CREATE"), []byte("bad1"), []byte("ON"), []byte("JSON"), []byte("SCHEMA"),
				[]byte("$.name"), []byte("AS"), []byte("name"), []byte("TEXT"), []byte("PHONETIC")},
			"SEARCH_PARSE_ARGS PHONETIC requires an argument",
		},
		{
			"matcher",
			[][]byte{[]byte("FT.CREATE"), []byte("bad2"), []byte("ON"), []byte("JSON"), []byte("SCHEMA"),
				[]byte("$.name"), []byte("AS"), []byte("name"), []byte("TEXT"), []byte("PHONETIC"), []byte("bad")},
			"SEARCH_QUERY_BAD Matcher Format: <2 chars algorithm>:<2 chars language>. Support algorithms: double metaphone (dm). Supported languages: English (en), French (fr), Portuguese (pt) and Spanish (es)",
		},
		{
			"after-sortable",
			[][]byte{[]byte("FT.CREATE"), []byte("bad3"), []byte("ON"), []byte("JSON"), []byte("SCHEMA"),
				[]byte("$.name"), []byte("AS"), []byte("name"), []byte("TEXT"), []byte("SORTABLE"), []byte("PHONETIC"), []byte("dm:en")},
			"SEARCH_PARSE_ARGS Invalid field type for field `PHONETIC`",
		},
		{
			"tag",
			[][]byte{[]byte("FT.CREATE"), []byte("bad4"), []byte("ON"), []byte("JSON"), []byte("SCHEMA"),
				[]byte("$.name"), []byte("AS"), []byte("name"), []byte("TAG"), []byte("PHONETIC"), []byte("dm:en")},
			"SEARCH_PARSE_ARGS Invalid field type for field `PHONETIC`",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Execute(tc.args)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err=%v want=%q", err, tc.want)
			}
		})
	}
}

func TestFTInfoPhoneticMatchesRedisMetadataShape(t *testing.T) {
	s := setupPhoneticSearch(t)

	reply, err := s.Execute([][]byte{[]byte("FT.INFO"), []byte("ph")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToUpper(string(reply)), "PHONETIC") {
		t.Fatalf("FT.INFO unexpectedly exposes PHONETIC: %q", reply)
	}
}

func TestFTSearchPhoneticDialectParity(t *testing.T) {
	s := setupPhoneticSearch(t)

	for _, query := range []string{"@name:jon", "jon"} {
		d1 := searchNoContentKeys(t, s, "ph", query, "DIALECT", "1")
		d2 := searchNoContentKeys(t, s, "ph", query, "DIALECT", "2")
		requireSearchKeys(t, d2, d1...)
	}
}
