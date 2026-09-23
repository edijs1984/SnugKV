package server

import (
	"strings"
	"testing"
)

func TestFTCreateSchemaModifiers(t *testing.T) {
	s := newSearchTestServer(t)

	for _, doc := range []struct {
		key  string
		json string
	}{
		{"doc:1", `{"title":"memory guide","category":"docs","price":10,"hidden":"alpha"}`},
		{"doc:2", `{"title":"server design","category":"infra","price":20,"hidden":"beta"}`},
		{"doc:3", `{"title":"memory engine","category":"docs","price":15,"hidden":"gamma"}`},
	} {
		if _, err := s.Execute([][]byte{
			[]byte("JSON.SET"), []byte(doc.key), []byte("$"), []byte(doc.json),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("modtxt"),
		[]byte("ON"), []byte("JSON"),
		[]byte("PREFIX"), []byte("1"), []byte("doc:"),
		[]byte("SCHEMA"),
		[]byte("$.title"), []byte("AS"), []byte("title"), []byte("TEXT"),
		[]byte("WEIGHT"), []byte("2.5"), []byte("SORTABLE"),
		[]byte("$.hidden"), []byte("AS"), []byte("hidden"), []byte("TEXT"), []byte("NOINDEX"),
	}); err != nil {
		t.Fatal(err)
	}

	info, err := s.Execute([][]byte{[]byte("FT.INFO"), []byte("modtxt")})
	if err != nil {
		t.Fatal(err)
	}
	gotInfo := string(info)
	for _, want := range []string{
		"$6\r\nWEIGHT\r\n$3\r\n2.5\r\n",
		"$8\r\nSORTABLE\r\n$3\r\nUNF\r\n",
		"$7\r\nNOINDEX\r\n",
	} {
		if !strings.Contains(gotInfo, want) {
			t.Fatalf("FT.INFO missing %q in %q", want, gotInfo)
		}
	}

	reply, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"), []byte("modtxt"), []byte("@hidden:alpha"), []byte("NOCONTENT"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(reply); got != "*1\r\n:0\r\n" {
		t.Fatalf("NOINDEX query reply=%q", got)
	}

	reply, err = s.Execute([][]byte{
		[]byte("FT.SEARCH"), []byte("modtxt"), []byte("*"),
		[]byte("SORTBY"), []byte("hidden"), []byte("ASC"), []byte("NOCONTENT"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(reply); got != "*4\r\n:3\r\n$5\r\ndoc:1\r\n$5\r\ndoc:2\r\n$5\r\ndoc:3\r\n" {
		t.Fatalf("NOINDEX sort reply=%q", got)
	}
}

func TestFTCreateWeightCompatibility(t *testing.T) {
	tests := []struct {
		name string
		args [][]byte
		want string
	}{
		{
			name: "missing value becomes zero",
			args: [][]byte{
				[]byte("FT.CREATE"), []byte("idx"),
				[]byte("ON"), []byte("JSON"), []byte("SCHEMA"),
				[]byte("$.title"), []byte("AS"), []byte("title"), []byte("TEXT"), []byte("WEIGHT"),
			},
			want: "$6\r\nWEIGHT\r\n$1\r\n0\r\n",
		},
		{
			name: "negative accepted",
			args: [][]byte{
				[]byte("FT.CREATE"), []byte("idx"),
				[]byte("ON"), []byte("JSON"), []byte("SCHEMA"),
				[]byte("$.title"), []byte("AS"), []byte("title"), []byte("TEXT"), []byte("WEIGHT"), []byte("-1"),
			},
			want: "$6\r\nWEIGHT\r\n$2\r\n-1\r\n",
		},
		{
			name: "zero accepted",
			args: [][]byte{
				[]byte("FT.CREATE"), []byte("idx"),
				[]byte("ON"), []byte("JSON"), []byte("SCHEMA"),
				[]byte("$.title"), []byte("AS"), []byte("title"), []byte("TEXT"), []byte("WEIGHT"), []byte("0"),
			},
			want: "$6\r\nWEIGHT\r\n$1\r\n0\r\n",
		},
		{
			name: "duplicate last wins",
			args: [][]byte{
				[]byte("FT.CREATE"), []byte("idx"),
				[]byte("ON"), []byte("JSON"), []byte("SCHEMA"),
				[]byte("$.title"), []byte("AS"), []byte("title"), []byte("TEXT"),
				[]byte("WEIGHT"), []byte("2"), []byte("WEIGHT"), []byte("3"),
			},
			want: "$6\r\nWEIGHT\r\n$1\r\n3\r\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newSearchTestServer(t)
			if _, err := s.Execute(tc.args); err != nil {
				t.Fatal(err)
			}
			info, err := s.Execute([][]byte{[]byte("FT.INFO"), []byte("idx")})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(info), tc.want) {
				t.Fatalf("FT.INFO=%q missing %q", info, tc.want)
			}
		})
	}

	s := newSearchTestServer(t)
	_, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("bad"),
		[]byte("ON"), []byte("JSON"), []byte("SCHEMA"),
		[]byte("$.title"), []byte("AS"), []byte("title"), []byte("TEXT"),
		[]byte("WEIGHT"), []byte("nope"),
	})
	if err == nil || err.Error() != "SEARCH_PARSE_ARGS Bad arguments for weight: Could not convert argument to expected type" {
		t.Fatalf("invalid weight err=%v", err)
	}
}

func TestFTCreateSchemaModifierOrdering(t *testing.T) {
	s := newSearchTestServer(t)

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("ok"),
		[]byte("ON"), []byte("JSON"), []byte("SCHEMA"),
		[]byte("$.title"), []byte("AS"), []byte("title"), []byte("TEXT"),
		[]byte("WEIGHT"), []byte("3"), []byte("NOINDEX"), []byte("SORTABLE"),
	}); err != nil {
		t.Fatalf("valid modifier ordering: %v", err)
	}

	_, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("bad"),
		[]byte("ON"), []byte("JSON"), []byte("SCHEMA"),
		[]byte("$.title"), []byte("AS"), []byte("title"), []byte("TEXT"),
		[]byte("SORTABLE"), []byte("WEIGHT"), []byte("3"),
	})
	if err == nil || err.Error() != "SEARCH_PARSE_ARGS Invalid field type for field `WEIGHT`" {
		t.Fatalf("invalid ordering err=%v", err)
	}
}

func TestFTCreateDuplicateBooleanModifiers(t *testing.T) {
	s := newSearchTestServer(t)

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("dups"),
		[]byte("ON"), []byte("JSON"), []byte("SCHEMA"),
		[]byte("$.title"), []byte("AS"), []byte("title"), []byte("TEXT"),
		[]byte("SORTABLE"), []byte("SORTABLE"), []byte("NOINDEX"), []byte("NOINDEX"),
	}); err != nil {
		t.Fatal(err)
	}

	info, err := s.Execute([][]byte{[]byte("FT.INFO"), []byte("dups")})
	if err != nil {
		t.Fatal(err)
	}
	got := string(info)
	if strings.Count(got, "$8\r\nSORTABLE\r\n") != 1 {
		t.Fatalf("SORTABLE duplicated in FT.INFO: %q", got)
	}
	if strings.Count(got, "$7\r\nNOINDEX\r\n") != 1 {
		t.Fatalf("NOINDEX duplicated in FT.INFO: %q", got)
	}
}
