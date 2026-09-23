package server

import "testing"

func TestFTSearchLanguageEnglishAndGerman(t *testing.T) {
	s := newSearchTestServer(t)

	for _, doc := range []struct {
		key  string
		json string
	}{
		{"lang:en:1", `{"text":"run"}`},
		{"lang:en:2", `{"text":"runs"}`},
		{"lang:en:3", `{"text":"running"}`},
		{"lang:de:1", `{"text":"haus"}`},
		{"lang:de:2", `{"text":"hauses"}`},
		{"lang:de:3", `{"text":"häuser"}`},
		{"lang:de:4", `{"text":"häusern"}`},
	} {
		if _, err := s.Execute([][]byte{
			[]byte("JSON.SET"), []byte(doc.key), []byte("$"), []byte(doc.json),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("langen"),
		[]byte("ON"), []byte("JSON"),
		[]byte("PREFIX"), []byte("1"), []byte("lang:en:"),
		[]byte("LANGUAGE"), []byte("english"),
		[]byte("SCHEMA"), []byte("$.text"), []byte("AS"), []byte("text"), []byte("TEXT"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("langde"),
		[]byte("ON"), []byte("JSON"),
		[]byte("PREFIX"), []byte("1"), []byte("lang:de:"),
		[]byte("LANGUAGE"), []byte("german"),
		[]byte("SCHEMA"), []byte("$.text"), []byte("AS"), []byte("text"), []byte("TEXT"),
	}); err != nil {
		t.Fatal(err)
	}

	reply, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"), []byte("langen"), []byte("@text:run"), []byte("NOCONTENT"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(reply), "*4\r\n:3\r\n$9\r\nlang:en:1\r\n$9\r\nlang:en:2\r\n$9\r\nlang:en:3\r\n"; got != want {
		t.Fatalf("english reply=%q want=%q", got, want)
	}

	reply, err = s.Execute([][]byte{
		[]byte("FT.SEARCH"), []byte("langde"), []byte("@text:haus"), []byte("NOCONTENT"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(reply), "*5\r\n:4\r\n$9\r\nlang:de:1\r\n$9\r\nlang:de:2\r\n$9\r\nlang:de:3\r\n$9\r\nlang:de:4\r\n"; got != want {
		t.Fatalf("german reply=%q want=%q", got, want)
	}

	if _, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"), []byte("langen"), []byte("@text:run"), []byte("NOCONTENT"),
		[]byte("LANGUAGE"), []byte("german"),
	}); err != nil {
		t.Fatalf("query language override: %v", err)
	}
}

func TestFTSearchLanguageField(t *testing.T) {
	s := newSearchTestServer(t)

	for _, doc := range []struct {
		key  string
		json string
	}{
		{"lang:mixed:1", `{"lang":"english","text":"running"}`},
		{"lang:mixed:2", `{"lang":"german","text":"häusern"}`},
		{"lang:mixed:3", `{"lang":"english","text":"studies"}`},
	} {
		if _, err := s.Execute([][]byte{
			[]byte("JSON.SET"), []byte(doc.key), []byte("$"), []byte(doc.json),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("langfield"),
		[]byte("ON"), []byte("JSON"),
		[]byte("PREFIX"), []byte("1"), []byte("lang:mixed:"),
		[]byte("LANGUAGE"), []byte("english"),
		[]byte("LANGUAGE_FIELD"), []byte("$.lang"),
		[]byte("SCHEMA"), []byte("$.text"), []byte("AS"), []byte("text"), []byte("TEXT"),
	}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		query string
		key   string
	}{
		{"@text:run", "lang:mixed:1"},
		{"@text:haus", "lang:mixed:2"},
		{"@text:study", "lang:mixed:3"},
	} {
		reply, err := s.Execute([][]byte{
			[]byte("FT.SEARCH"), []byte("langfield"), []byte(tc.query), []byte("NOCONTENT"),
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.query, err)
		}
		want := "*2\r\n:1\r\n$12\r\n" + tc.key + "\r\n"
		if got := string(reply); got != want {
			t.Fatalf("%s reply=%q want=%q", tc.query, got, want)
		}
	}

	reply, err := s.Execute([][]byte{[]byte("FT.INFO"), []byte("langfield")})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(reply); !containsRESPBulk(got, "language_field") || !containsRESPBulk(got, "$.lang") {
		t.Fatalf("FT.INFO missing language_field: %q", got)
	}
}

func TestFTSearchLanguageValidation(t *testing.T) {
	s := newSearchTestServer(t)

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("badlang"),
		[]byte("ON"), []byte("JSON"),
		[]byte("LANGUAGE"), []byte("klingon"),
		[]byte("SCHEMA"), []byte("$.text"), []byte("AS"), []byte("text"), []byte("TEXT"),
	}); err == nil || err.Error() != "SEARCH_ADD_ARGS Invalid language" {
		t.Fatalf("invalid index language error=%v", err)
	}

	if _, err := s.Execute([][]byte{
		[]byte("JSON.SET"), []byte("lang:en:1"), []byte("$"), []byte(`{"text":"run"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("langen"),
		[]byte("ON"), []byte("JSON"),
		[]byte("PREFIX"), []byte("1"), []byte("lang:en:"),
		[]byte("SCHEMA"), []byte("$.text"), []byte("AS"), []byte("text"), []byte("TEXT"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"), []byte("langen"), []byte("@text:run"), []byte("NOCONTENT"),
		[]byte("LANGUAGE"), []byte("klingon"),
	}); err == nil || err.Error() != "SEARCH_QUERY_BAD No such language" {
		t.Fatalf("invalid query language error=%v", err)
	}
}

func containsRESPBulk(reply, value string) bool {
	return len(value) > 0 && len(reply) > 0 && stringContains(reply, value)
}

func stringContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
