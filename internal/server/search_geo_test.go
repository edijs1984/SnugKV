package server

import (
	"strings"
	"testing"
)

func setupGeoSearch(t *testing.T) *Server {
	t.Helper()
	s := newSearchTestServer(t)

	docs := []struct {
		key   string
		value string
	}{
		{"place:1", `{"name":"Riga","loc":"24.1052,56.9496","rank":30}`},
		{"place:2", `{"name":"Jurmala","loc":"23.7704,56.9680","rank":20}`},
		{"place:3", `{"name":"Sigulda","loc":"24.8595,57.1537","rank":40}`},
		{"place:4", `{"name":"Jelgava","loc":"23.7128,56.6511","rank":10}`},
		{"place:5", `{"name":"No location","rank":50}`},
		{"place:6", `{"name":"Bad location","loc":"not-a-coordinate","rank":60}`},
	}
	for _, doc := range docs {
		if _, err := s.Execute([][]byte{
			[]byte("JSON.SET"), []byte(doc.key), []byte("$"), []byte(doc.value),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("geo"),
		[]byte("ON"), []byte("JSON"),
		[]byte("PREFIX"), []byte("1"), []byte("place:"),
		[]byte("SCHEMA"),
		[]byte("$.name"), []byte("AS"), []byte("name"), []byte("TEXT"),
		[]byte("$.loc"), []byte("AS"), []byte("loc"), []byte("GEO"),
		[]byte("$.rank"), []byte("AS"), []byte("rank"), []byte("NUMERIC"), []byte("SORTABLE"),
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFTSearchGeoRadiusAndUnits(t *testing.T) {
	s := setupGeoSearch(t)

	requireSearchKeys(t, searchNoContentKeys(t, s, "geo", "@loc:[24.1052 56.9496 1 km]"),
		"place:1")
	requireSearchKeys(t, searchNoContentKeys(t, s, "geo", "@loc:[24.1052 56.9496 30 km]"),
		"place:1", "place:2")
	requireSearchKeys(t, searchNoContentKeys(t, s, "geo", "@loc:[24.1052 56.9496 60 km]"),
		"place:1", "place:2", "place:3", "place:4")

	for _, query := range []string{
		"@loc:[24.1052 56.9496 30000 m]",
		"@loc:[24.1052 56.9496 20 mi]",
		"@loc:[24.1052 56.9496 100000 ft]",
	} {
		requireSearchKeys(t, searchNoContentKeys(t, s, "geo", query),
			"place:1", "place:2")
	}
}

func TestFTSearchGeoBooleanSortAndLimit(t *testing.T) {
	s := setupGeoSearch(t)

	requireSearchKeys(t,
		searchNoContentKeys(t, s, "geo", "@loc:[24.1052 56.9496 60 km] @rank:[0 25]"),
		"place:2", "place:4")

	requireSearchKeys(t,
		searchNoContentKeys(t, s, "geo", "(@loc:[24.1052 56.9496 60 km]) | (@rank:[40 40])", "DIALECT", "2"),
		"place:1", "place:2", "place:3", "place:4")

	requireSearchKeys(t,
		searchNoContentKeys(t, s, "geo", "-@loc:[24.1052 56.9496 30 km]", "DIALECT", "2"),
		"place:3", "place:4", "place:5")

	requireSearchKeys(t,
		searchNoContentKeys(t, s, "geo", "@loc:[24.1052 56.9496 100 km]", "SORTBY", "rank", "ASC"),
		"place:4", "place:2", "place:1", "place:3")

	requireSearchKeys(t,
		searchNoContentKeys(t, s, "geo", "@loc:[24.1052 56.9496 100 km]", "LIMIT", "1", "2"),
		"place:2", "place:3")
}

func TestFTSearchGeoMutationVisibility(t *testing.T) {
	s := setupGeoSearch(t)

	if _, err := s.Execute([][]byte{
		[]byte("JSON.SET"), []byte("place:2"), []byte("$.loc"), []byte(`"26.0000,56.9680"`),
	}); err != nil {
		t.Fatal(err)
	}
	requireSearchKeys(t, searchNoContentKeys(t, s, "geo", "@loc:[24.1052 56.9496 30 km]"),
		"place:1")

	if _, err := s.Execute([][]byte{
		[]byte("JSON.SET"), []byte("place:2"), []byte("$.loc"), []byte(`"23.7704,56.9680"`),
	}); err != nil {
		t.Fatal(err)
	}
	requireSearchKeys(t, searchNoContentKeys(t, s, "geo", "@loc:[24.1052 56.9496 30 km]"),
		"place:1", "place:2")

	if _, err := s.Execute([][]byte{
		[]byte("JSON.DEL"), []byte("place:2"), []byte("$.loc"),
	}); err != nil {
		t.Fatal(err)
	}
	requireSearchKeys(t, searchNoContentKeys(t, s, "geo", "@loc:[24.1052 56.9496 30 km]"),
		"place:1")
}

func TestFTSearchGeoStringOnlyAndBounds(t *testing.T) {
	s := newSearchTestServer(t)

	for _, doc := range []struct{ key, value string }{
		{"alt:1", `{"loc":[24.1052,56.9496]}`},
		{"alt:2", `{"loc":{"lon":24.1052,"lat":56.9496}}`},
	} {
		if _, err := s.Execute([][]byte{
			[]byte("JSON.SET"), []byte(doc.key), []byte("$"), []byte(doc.value),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("alt"), []byte("ON"), []byte("JSON"),
		[]byte("PREFIX"), []byte("1"), []byte("alt:"),
		[]byte("SCHEMA"), []byte("$.loc"), []byte("AS"), []byte("loc"), []byte("GEO"),
	}); err != nil {
		t.Fatal(err)
	}
	requireSearchKeys(t, searchNoContentKeys(t, s, "alt", "@loc:[24.1052 56.9496 1 km]"))

	for _, doc := range []struct{ key, value string }{
		{"edge:1", `{"loc":"180,85"}`},
		{"edge:2", `{"loc":"-180,-85"}`},
		{"edge:3", `{"loc":"181,0"}`},
		{"edge:4", `{"loc":"0,86"}`},
	} {
		if _, err := s.Execute([][]byte{
			[]byte("JSON.SET"), []byte(doc.key), []byte("$"), []byte(doc.value),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("edge"), []byte("ON"), []byte("JSON"),
		[]byte("PREFIX"), []byte("1"), []byte("edge:"),
		[]byte("SCHEMA"), []byte("$.loc"), []byte("AS"), []byte("loc"), []byte("GEO"),
	}); err != nil {
		t.Fatal(err)
	}
	requireSearchKeys(t, searchNoContentKeys(t, s, "edge", "@loc:[180 85 1 km]"), "edge:1")
	requireSearchKeys(t, searchNoContentKeys(t, s, "edge", "@loc:[-180 -85 1 km]"), "edge:2")
}

func TestFTSearchGeoParserErrorsAndSchema(t *testing.T) {
	s := setupGeoSearch(t)

	requireSearchKeys(t, searchNoContentKeys(t, s, "geo", "@loc:[24.1052 56.9496]"))
	requireSearchKeys(t, searchNoContentKeys(t, s, "geo", "@missing:[24.1052 56.9496 30 km]"))

	cases := []struct {
		query string
		want  string
	}{
		{"@loc:[24.1052 56.9496 30]", "SEARCH_SYNTAX Syntax error at offset 24 near 30"},
		{"@loc:[x 56.9496 30 km]", "SEARCH_SYNTAX Syntax error at offset 6 near x"},
		{"@loc:[24.1052 y 30 km]", "SEARCH_SYNTAX Syntax error at offset 14 near y"},
		{"@loc:[24.1052 56.9496 x km]", "SEARCH_SYNTAX Syntax error at offset 22 near x"},
		{"@loc:[24.1052 56.9496 -1 km]", "SEARCH_SYNTAX Invalid GeoFilter radius"},
		{"@loc:[24.1052 56.9496 30 bad]", "SEARCH_SYNTAX Invalid GeoFilter unit"},
	}
	for _, tc := range cases {
		_, err := s.Execute([][]byte{
			[]byte("FT.SEARCH"), []byte("geo"), []byte(tc.query), []byte("NOCONTENT"),
		})
		if err == nil || err.Error() != tc.want {
			t.Fatalf("query=%q err=%v want=%q", tc.query, err, tc.want)
		}
	}

	reply, err := s.Execute([][]byte{
		[]byte("FT.INFO"), []byte("geo"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reply), "GEO") {
		t.Fatalf("FT.INFO missing GEO: %q", reply)
	}

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("geo-sort"), []byte("ON"), []byte("JSON"),
		[]byte("PREFIX"), []byte("1"), []byte("place:"),
		[]byte("SCHEMA"), []byte("$.loc"), []byte("AS"), []byte("loc"), []byte("GEO"), []byte("SORTABLE"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("geo-noindex"), []byte("ON"), []byte("JSON"),
		[]byte("PREFIX"), []byte("1"), []byte("place:"),
		[]byte("SCHEMA"), []byte("$.loc"), []byte("AS"), []byte("loc"), []byte("GEO"), []byte("NOINDEX"),
	}); err != nil {
		t.Fatal(err)
	}

	for _, index := range []string{"geo-sort", "geo-noindex"} {
		info, err := s.Execute([][]byte{[]byte("FT.INFO"), []byte(index)})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(info), "$8\r\nnum_docs\r\n:5\r\n") {
			t.Fatalf("%s FT.INFO num_docs mismatch: %q", index, info)
		}
	}
}
