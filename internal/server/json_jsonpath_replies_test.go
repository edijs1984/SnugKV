package server

import (
	"testing"

	"snugkv/internal/engine"
)

const jsonReplyDoc = `{"a":1,"s":"hi","on":true,"arr":[1,2,3],"o":{"x":1,"y":2},"n":{"arr":[7]},"list":[{"v":1},{"v":2},{"v":"z"}]}`

func jsonReplyServer(t *testing.T) *Server {
	t.Helper()
	s := New(engine.New())
	if got := execute(t, s, "JSON.SET", "doc", "$", jsonReplyDoc); got != "+OK\r\n" {
		t.Fatal(got)
	}
	return s
}

func TestJSONPathRepliesAreOneEntryPerMatch(t *testing.T) {
	s := jsonReplyServer(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		// reads
		{[]string{"JSON.ARRLEN", "doc", "$.arr"}, "*1\r\n:3\r\n"},
		{[]string{"JSON.ARRLEN", "doc", "$..arr"}, "*2\r\n:3\r\n:1\r\n"},
		{[]string{"JSON.ARRLEN", "doc", "$.a"}, "*1\r\n$-1\r\n"},
		{[]string{"JSON.ARRLEN", "doc", "$.nope"}, "*0\r\n"},
		{[]string{"JSON.STRLEN", "doc", "$.s"}, "*1\r\n:2\r\n"},
		{[]string{"JSON.OBJLEN", "doc", "$.o"}, "*1\r\n:2\r\n"},
		{[]string{"JSON.OBJKEYS", "doc", "$.o"}, "*1\r\n*2\r\n$1\r\nx\r\n$1\r\ny\r\n"},
		{[]string{"JSON.OBJKEYS", "doc", "$.a"}, "*1\r\n$-1\r\n"},
		{[]string{"JSON.ARRINDEX", "doc", "$.arr", "2"}, "*1\r\n:1\r\n"},
		{[]string{"JSON.ARRINDEX", "doc", "$.arr", "9"}, "*1\r\n:-1\r\n"},
		{[]string{"JSON.ARRINDEX", "doc", "$.arr", "3", "0", "0"}, "*1\r\n:2\r\n"},
		{[]string{"JSON.ARRINDEX", "doc", "$.arr", "3", "0", "2"}, "*1\r\n:-1\r\n"},
		// legacy paths keep the single-value reply
		{[]string{"JSON.ARRLEN", "doc", ".arr"}, ":3\r\n"},
		{[]string{"JSON.OBJKEYS", "doc", ".o"}, "*2\r\n$1\r\nx\r\n$1\r\ny\r\n"},
		// writes
		{[]string{"JSON.NUMINCRBY", "doc", "$.a", "2"}, "$3\r\n[3]\r\n"},
		{[]string{"JSON.NUMINCRBY", "doc", "$.list[*].v", "10"}, "$12\r\n[11,12,null]\r\n"},
		{[]string{"JSON.NUMMULTBY", "doc", "$.a", "2"}, "$3\r\n[6]\r\n"},
		{[]string{"JSON.TOGGLE", "doc", "$.on"}, "*1\r\n:0\r\n"},
		{[]string{"JSON.TOGGLE", "doc", "$.a"}, "*1\r\n$-1\r\n"},
		{[]string{"JSON.STRAPPEND", "doc", "$.s", `"!"`}, "*1\r\n:3\r\n"},
		{[]string{"JSON.ARRAPPEND", "doc", "$..arr", "5"}, "*2\r\n:4\r\n:2\r\n"},
		{[]string{"JSON.ARRINSERT", "doc", "$.arr", "0", "0"}, "*1\r\n:5\r\n"},
		{[]string{"JSON.ARRPOP", "doc", "$.arr"}, "*1\r\n$1\r\n5\r\n"},
		{[]string{"JSON.ARRPOP", "doc", "$.arr", "0"}, "*1\r\n$1\r\n0\r\n"},
		{[]string{"JSON.ARRTRIM", "doc", "$.arr", "0", "1"}, "*1\r\n:2\r\n"},
		{[]string{"JSON.ARRPOP", "doc", "$.nope"}, "*0\r\n"},
		{[]string{"JSON.CLEAR", "doc", "$..arr"}, ":2\r\n"},
		{[]string{"JSON.GET", "doc", "$..arr"}, "$7\r\n[[],[]]\r\n"},
		// a missing key is null
		{[]string{"JSON.ARRLEN", "missing", "$.arr"}, "$-1\r\n"},
		{[]string{"JSON.NUMINCRBY", "missing", "$.a", "1"}, "$-1\r\n"},
	} {
		if got := execute(t, s, tc.args...); got != tc.want {
			t.Fatalf("%q\n got %q\nwant %q", tc.args, got, tc.want)
		}
	}
}

func TestJSONUpdateWithAnErrorChangesNothing(t *testing.T) {
	s := jsonReplyServer(t)
	before := execute(t, s, "JSON.GET", "doc", "$")
	if _, err := s.Execute([][]byte{[]byte("JSON.ARRINSERT"), []byte("doc"), []byte("$..arr"), []byte("9"), []byte("1")}); err == nil {
		t.Fatal("expected index out of bounds")
	}
	if after := execute(t, s, "JSON.GET", "doc", "$"); after != before {
		t.Fatalf("document changed: %q -> %q", before, after)
	}
}

func TestJSONGetSeveralPaths(t *testing.T) {
	s := jsonReplyServer(t)
	if got := execute(t, s, "JSON.GET", "doc", "$.a", "$.s", "$.nope"); got != "$36\r\n{\"$.a\":[1],\"$.s\":[\"hi\"],\"$.nope\":[]}\r\n" {
		t.Fatalf("got %q", got)
	}
	if got := execute(t, s, "JSON.GET", "doc", ".a", ".s"); got != "$18\r\n{\".a\":1,\".s\":\"hi\"}\r\n" {
		t.Fatalf("legacy got %q", got)
	}
	if got := execute(t, s, "JSON.GET", "missing", "$.a", "$.b"); got != "$-1\r\n" {
		t.Fatalf("missing key got %q", got)
	}
}
