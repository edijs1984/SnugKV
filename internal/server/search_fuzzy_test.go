package server

import "testing"

func TestFTSearchFuzzyText(t *testing.T) {
	s := newSearchTestServer(t)

	for _, doc := range []struct {
		key  string
		json string
	}{
		{"fuzzy:1", `{"text":"memory guide"}`},
		{"fuzzy:2", `{"text":"memori guide"}`},
		{"fuzzy:3", `{"text":"memry manual"}`},
		{"fuzzy:4", `{"text":"server design"}`},
		{"fuzzy:5", `{"text":"running system"}`},
		{"fuzzy:6", `{"text":"run system"}`},
	} {
		if _, err := s.Execute([][]byte{
			[]byte("JSON.SET"), []byte(doc.key), []byte("$"), []byte(doc.json),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("fuzzyidx"),
		[]byte("ON"), []byte("JSON"),
		[]byte("PREFIX"), []byte("1"), []byte("fuzzy:"),
		[]byte("SCHEMA"), []byte("$.text"), []byte("AS"), []byte("text"), []byte("TEXT"),
	}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		query string
		want  string
	}{
		{"@text:%memory%", "*4\r\n:3\r\n$7\r\nfuzzy:1\r\n$7\r\nfuzzy:2\r\n$7\r\nfuzzy:3\r\n"},
		{"@text:%%memory%%", "*4\r\n:3\r\n$7\r\nfuzzy:1\r\n$7\r\nfuzzy:2\r\n$7\r\nfuzzy:3\r\n"},
		{"@text:%%%memory%%%", "*4\r\n:3\r\n$7\r\nfuzzy:1\r\n$7\r\nfuzzy:2\r\n$7\r\nfuzzy:3\r\n"},
		{"@text:%memori%", "*3\r\n:2\r\n$7\r\nfuzzy:1\r\n$7\r\nfuzzy:2\r\n"},
		{"@text:%memry%", "*3\r\n:2\r\n$7\r\nfuzzy:1\r\n$7\r\nfuzzy:3\r\n"},
		{"@text:%run%", "*3\r\n:2\r\n$7\r\nfuzzy:5\r\n$7\r\nfuzzy:6\r\n"},
		{"@text:%running%", "*2\r\n:1\r\n$7\r\nfuzzy:5\r\n"},
		{"@text:(%memory% guide)", "*3\r\n:2\r\n$7\r\nfuzzy:1\r\n$7\r\nfuzzy:2\r\n"},
		{"@text:(%memory% %guide%)", "*3\r\n:2\r\n$7\r\nfuzzy:1\r\n$7\r\nfuzzy:2\r\n"},
	}

	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			reply, err := s.Execute([][]byte{
				[]byte("FT.SEARCH"), []byte("fuzzyidx"), []byte(tc.query), []byte("NOCONTENT"),
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := string(reply); got != tc.want {
				t.Fatalf("reply=%q want=%q", got, tc.want)
			}
		})
	}

	reply, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"), []byte("fuzzyidx"), []byte("@missing:%memory%"), []byte("NOCONTENT"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(reply); got != "*1\r\n:0\r\n" {
		t.Fatalf("unknown field reply=%q", got)
	}
}

func TestFTSearchFuzzyValidation(t *testing.T) {
	s := newSearchTestServer(t)

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("fuzzyidx"),
		[]byte("ON"), []byte("JSON"),
		[]byte("SCHEMA"), []byte("$.text"), []byte("AS"), []byte("text"), []byte("TEXT"),
	}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		query string
		want  string
	}{
		{"@text:%mem*%", "SEARCH_SYNTAX Syntax error at offset 7 near mem"},
		{"@text:%", "SEARCH_SYNTAX Syntax error at offset 6 near text"},
		{"@text:%%", "SEARCH_SYNTAX Syntax error at offset 7 near text"},
		{"@text:%%%%memory%%%%", "SEARCH_SYNTAX Syntax error at offset 9 near text"},
		{"@text:%memory%%", "SEARCH_SYNTAX Syntax error at offset 14 near memory"},
		{"@text:memory%", "SEARCH_SYNTAX Syntax error at offset 12 near memory"},
		{"@text:%memory", "SEARCH_SYNTAX Syntax error at offset 7 near memory"},
	}

	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			_, err := s.Execute([][]byte{
				[]byte("FT.SEARCH"), []byte("fuzzyidx"), []byte(tc.query), []byte("NOCONTENT"),
			})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err=%v want=%q", err, tc.want)
			}
		})
	}
}
