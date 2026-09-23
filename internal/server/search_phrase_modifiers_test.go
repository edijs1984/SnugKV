package server

import "testing"

func TestFTSearchTextSlopAndInOrder(t *testing.T) {
	s := newSearchTestServer(t)

	for _, doc := range []struct {
		key  string
		json string
	}{
		{"phrase:1", `{"text":"memory guide"}`},
		{"phrase:2", `{"text":"memory fast guide"}`},
		{"phrase:3", `{"text":"guide memory"}`},
		{"phrase:4", `{"text":"memory very fast guide"}`},
		{"phrase:5", `{"text":"memory search guide"}`},
	} {
		if _, err := s.Execute([][]byte{
			[]byte("JSON.SET"), []byte(doc.key), []byte("$"), []byte(doc.json),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("phraseidx"),
		[]byte("ON"), []byte("JSON"),
		[]byte("PREFIX"), []byte("1"), []byte("phrase:"),
		[]byte("SCHEMA"), []byte("$.text"), []byte("AS"), []byte("text"), []byte("TEXT"),
	}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		args [][]byte
		want string
	}{
		{
			name: "slop zero unordered",
			args: [][]byte{[]byte("FT.SEARCH"), []byte("phraseidx"), []byte("@text:(memory guide)"), []byte("NOCONTENT"), []byte("SLOP"), []byte("0")},
			want: "*3\r\n:2\r\n$8\r\nphrase:1\r\n$8\r\nphrase:3\r\n",
		},
		{
			name: "slop one unordered",
			args: [][]byte{[]byte("FT.SEARCH"), []byte("phraseidx"), []byte("@text:(memory guide)"), []byte("NOCONTENT"), []byte("SLOP"), []byte("1")},
			want: "*5\r\n:4\r\n$8\r\nphrase:1\r\n$8\r\nphrase:2\r\n$8\r\nphrase:3\r\n$8\r\nphrase:5\r\n",
		},
		{
			name: "slop one inorder",
			args: [][]byte{[]byte("FT.SEARCH"), []byte("phraseidx"), []byte("@text:(memory guide)"), []byte("NOCONTENT"), []byte("SLOP"), []byte("1"), []byte("INORDER")},
			want: "*4\r\n:3\r\n$8\r\nphrase:1\r\n$8\r\nphrase:2\r\n$8\r\nphrase:5\r\n",
		},
		{
			name: "quoted phrase remains exact",
			args: [][]byte{[]byte("FT.SEARCH"), []byte("phraseidx"), []byte(`@text:"memory guide"`), []byte("NOCONTENT"), []byte("SLOP"), []byte("2")},
			want: "*2\r\n:1\r\n$8\r\nphrase:1\r\n",
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

func TestFTSearchSlopValidation(t *testing.T) {
	s := newSearchTestServer(t)

	if _, err := s.Execute([][]byte{
		[]byte("FT.CREATE"), []byte("phraseidx"),
		[]byte("ON"), []byte("JSON"),
		[]byte("SCHEMA"), []byte("$.text"), []byte("AS"), []byte("text"), []byte("TEXT"),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"), []byte("phraseidx"), []byte(`@text:"memory guide"`),
		[]byte("NOCONTENT"), []byte("SLOP"),
	}); err == nil || err.Error() != "SEARCH_PARSE_ARGS Bad arguments for SLOP: Expected an argument, but none provided" {
		t.Fatalf("missing slop error=%v", err)
	}

	if _, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"), []byte("phraseidx"), []byte(`@text:"memory guide"`),
		[]byte("NOCONTENT"), []byte("SLOP"), []byte("nope"),
	}); err == nil || err.Error() != "SEARCH_PARSE_ARGS Bad arguments for SLOP: Could not convert argument to expected type" {
		t.Fatalf("bad slop error=%v", err)
	}

	if _, err := s.Execute([][]byte{
		[]byte("FT.SEARCH"), []byte("phraseidx"), []byte(`@text:"memory guide"`),
		[]byte("NOCONTENT"), []byte("INORDER"), []byte("extra"),
	}); err == nil || err.Error() != "SEARCH_ARG_UNRECOGNIZED Unknown argument `extra` at position 3 for <main>" {
		t.Fatalf("unknown argument error=%v", err)
	}
}
