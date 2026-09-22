package server

import (
	"snugkv/internal/engine"
	"strings"
	"testing"
)

func TestJSONExtendedCommands(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "JSON.SET", "doc", "$", `{"count":2,"price":1.5,"name":"Edijs","items":[1,2,3],"user":{"a":1,"b":2},"flag":true}`); got != "+OK\r\n" {
		t.Fatal(got)
	}

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"JSON.STRLEN", "doc", ".name"}, ":5\r\n"},
		{[]string{"JSON.ARRLEN", "doc", ".items"}, ":3\r\n"},
		{[]string{"JSON.OBJLEN", "doc", ".user"}, ":2\r\n"},
		{[]string{"JSON.STRLEN", "doc", ".count"}, "$-1\r\n"},
		{[]string{"JSON.ARRLEN", "doc", ".flag"}, "$-1\r\n"},
		{[]string{"JSON.OBJLEN", "doc", ".items"}, "$-1\r\n"},
		{[]string{"JSON.STRLEN", "doc", ".missing"}, "$-1\r\n"},
		{[]string{"JSON.NUMINCRBY", "doc", ".count", "3"}, "$1\r\n5\r\n"},
		{[]string{"JSON.NUMINCRBY", "doc", ".price", "0.25"}, "$4\r\n1.75\r\n"},
		{[]string{"JSON.GET", "doc", ".count"}, "$1\r\n5\r\n"},
		{[]string{"JSON.GET", "doc", ".price"}, "$4\r\n1.75\r\n"},
		{[]string{"JSON.NUMINCRBY", "doc", ".name", "1"}, "$-1\r\n"},
	} {
		if got := execute(t, s, tc.args...); got != tc.want {
			t.Fatalf("%q got %q want %q", tc.args, got, tc.want)
		}
	}
}

func TestJSONNumIncrByPreservesTTL(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "JSON.SET", "doc", "$", `{"count":1}`)
	execute(t, s, "PEXPIRE", "doc", "60000")
	execute(t, s, "JSON.NUMINCRBY", "doc", ".count", "2")

	got := execute(t, s, "PTTL", "doc")
	if strings.HasPrefix(got, ":-") {
		t.Fatalf("JSON.NUMINCRBY lost TTL: %q", got)
	}
}

func TestJSONNumIncrByRejectsInvalidNumber(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "JSON.SET", "doc", "$", `{"count":1}`)

	for _, increment := range []string{"NaN", "Inf", "-Inf", "not-a-number"} {
		_, err := s.Execute([][]byte{
			[]byte("JSON.NUMINCRBY"),
			[]byte("doc"),
			[]byte(".count"),
			[]byte(increment),
		})
		if err == nil {
			t.Fatalf("accepted increment %q", increment)
		}
	}

	if got := execute(t, s, "JSON.GET", "doc", ".count"); got != "$1\r\n1\r\n" {
		t.Fatalf("invalid increment mutated value: %q", got)
	}
}


func TestJSONAppendObjectKeysAndToggle(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "JSON.SET", "doc", "$", `{"name":"E","items":[1],"user":{"b":2,"a":1},"flag":true}`)

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"JSON.STRAPPEND", "doc", ".name", `"dijs"`}, ":5\r\n"},
		{[]string{"JSON.GET", "doc", ".name"}, "$7\r\n\"Edijs\"\r\n"},
		{[]string{"JSON.ARRAPPEND", "doc", ".items", "2", `{"x":1}`}, ":3\r\n"},
		{[]string{"JSON.GET", "doc", ".items"}, "$13\r\n[1,2,{\"x\":1}]\r\n"},
		{[]string{"JSON.OBJKEYS", "doc", ".user"}, "*2\r\n$1\r\na\r\n$1\r\nb\r\n"},
		{[]string{"JSON.TOGGLE", "doc", ".flag"}, ":0\r\n"},
		{[]string{"JSON.GET", "doc", ".flag"}, "$5\r\nfalse\r\n"},
		{[]string{"JSON.TOGGLE", "doc", ".flag"}, ":1\r\n"},
		{[]string{"JSON.GET", "doc", ".flag"}, "$4\r\ntrue\r\n"},
		{[]string{"JSON.ARRAPPEND", "doc", ".name", "1"}, "$-1\r\n"},
		{[]string{"JSON.STRAPPEND", "doc", ".items", `"x"`}, "$-1\r\n"},
		{[]string{"JSON.OBJKEYS", "doc", ".items"}, "$-1\r\n"},
		{[]string{"JSON.TOGGLE", "doc", ".name"}, "$-1\r\n"},
	} {
		if got := execute(t, s, tc.args...); got != tc.want {
			t.Fatalf("%q got %q want %q", tc.args, got, tc.want)
		}
	}
}

func TestJSONSecondBatchPreservesTTL(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "JSON.SET", "doc", "$", `{"name":"a","items":[],"flag":false}`)
	execute(t, s, "PEXPIRE", "doc", "60000")

	execute(t, s, "JSON.STRAPPEND", "doc", ".name", `"b"`)
	execute(t, s, "JSON.ARRAPPEND", "doc", ".items", "1")
	execute(t, s, "JSON.TOGGLE", "doc", ".flag")

	if got := execute(t, s, "PTTL", "doc"); strings.HasPrefix(got, ":-") {
		t.Fatalf("JSON mutation lost TTL: %q", got)
	}
}

func TestJSONStrAppendRequiresJSONString(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "JSON.SET", "doc", "$", `{"name":"a"}`)

	for _, value := range []string{"1", "true", "null", "{}"} {
		_, err := s.Execute([][]byte{
			[]byte("JSON.STRAPPEND"),
			[]byte("doc"),
			[]byte(".name"),
			[]byte(value),
		})
		if err == nil {
			t.Fatalf("accepted non-string append value %q", value)
		}
	}

	if got := execute(t, s, "JSON.GET", "doc", ".name"); got != "$3\r\n\"a\"\r\n" {
		t.Fatalf("invalid append mutated value: %q", got)
	}
}


func TestJSONArrayPopInsertIndexAndClear(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "JSON.SET", "doc", "$", `{"arr":[1,2,3],"obj":{"a":1},"num":7,"name":"x"}`)

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"JSON.ARRINDEX", "doc", ".arr", "2"}, ":1\r\n"},
		{[]string{"JSON.ARRINDEX", "doc", ".arr", "9"}, ":-1\r\n"},
		{[]string{"JSON.ARRINDEX", "doc", ".arr", "3", "0", "1"}, ":-1\r\n"},
		{[]string{"JSON.ARRINSERT", "doc", ".arr", "1", "9", "8"}, ":5\r\n"},
		{[]string{"JSON.GET", "doc", ".arr"}, "$11\r\n[1,9,8,2,3]\r\n"},
		{[]string{"JSON.ARRPOP", "doc", ".arr"}, "$1\r\n3\r\n"},
		{[]string{"JSON.ARRPOP", "doc", ".arr", "1"}, "$1\r\n9\r\n"},
		{[]string{"JSON.GET", "doc", ".arr"}, "$7\r\n[1,8,2]\r\n"},
		{[]string{"JSON.CLEAR", "doc", ".arr"}, ":1\r\n"},
		{[]string{"JSON.GET", "doc", ".arr"}, "$2\r\n[]\r\n"},
		{[]string{"JSON.CLEAR", "doc", ".obj"}, ":1\r\n"},
		{[]string{"JSON.GET", "doc", ".obj"}, "$2\r\n{}\r\n"},
		{[]string{"JSON.CLEAR", "doc", ".num"}, ":1\r\n"},
		{[]string{"JSON.GET", "doc", ".num"}, "$1\r\n0\r\n"},
		{[]string{"JSON.CLEAR", "doc", ".name"}, ":0\r\n"},
	} {
		if got := execute(t, s, tc.args...); got != tc.want {
			t.Fatalf("%q got %q want %q", tc.args, got, tc.want)
		}
	}
}

func TestJSONArrayMutationPreservesTTL(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "JSON.SET", "doc", "$", `{"arr":[1,2],"obj":{"a":1},"num":2}`)
	execute(t, s, "PEXPIRE", "doc", "60000")

	execute(t, s, "JSON.ARRINSERT", "doc", ".arr", "1", "9")
	execute(t, s, "JSON.ARRPOP", "doc", ".arr", "0")
	execute(t, s, "JSON.CLEAR", "doc", ".obj")

	if got := execute(t, s, "PTTL", "doc"); strings.HasPrefix(got, ":-") {
		t.Fatalf("JSON array/clear mutation lost TTL: %q", got)
	}
}

func TestJSONArrayInsertOutOfBoundsDoesNotMutate(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "JSON.SET", "doc", "$", `{"arr":[1,2]}`)

	_, err := s.Execute([][]byte{
		[]byte("JSON.ARRINSERT"),
		[]byte("doc"),
		[]byte(".arr"),
		[]byte("99"),
		[]byte("3"),
	})
	if err == nil {
		t.Fatal("expected out-of-bounds error")
	}

	if got := execute(t, s, "JSON.GET", "doc", ".arr"); got != "$5\r\n[1,2]\r\n" {
		t.Fatalf("out-of-bounds insert mutated value: %q", got)
	}
}


func TestJSONArrTrimMGetAndMerge(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "JSON.SET", "doc1", "$", `{"arr":[1,2,3,4],"obj":{"a":1,"b":2},"name":"one"}`)
	execute(t, s, "JSON.SET", "doc2", "$", `{"arr":[5,6],"obj":{"a":9},"name":"two"}`)

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"JSON.ARRTRIM", "doc1", ".arr", "1", "2"}, ":2\r\n"},
		{[]string{"JSON.GET", "doc1", ".arr"}, "$5\r\n[2,3]\r\n"},
		{[]string{"JSON.MGET", "doc1", "doc2", "missing", ".name"}, "*3\r\n$5\r\n\"one\"\r\n$5\r\n\"two\"\r\n$-1\r\n"},
		{[]string{"JSON.MERGE", "doc1", ".obj", `{"b":null,"c":3}`}, "+OK\r\n"},
		{[]string{"JSON.GET", "doc1", ".obj"}, "$13\r\n{\"a\":1,\"c\":3}\r\n"},
		{[]string{"JSON.MERGE", "doc1", ".newField", `{"x":1}`}, "+OK\r\n"},
		{[]string{"JSON.GET", "doc1", ".newField"}, "$7\r\n{\"x\":1}\r\n"},
		{[]string{"JSON.MERGE", "doc1", ".arr", `[8,9]`}, "+OK\r\n"},
		{[]string{"JSON.GET", "doc1", ".arr"}, "$5\r\n[8,9]\r\n"},
	} {
		if got := execute(t, s, tc.args...); got != tc.want {
			t.Fatalf("%q got %q want %q", tc.args, got, tc.want)
		}
	}
}

func TestJSONArrTrimForgivingBounds(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "JSON.SET", "doc", "$", `{"arr":[1,2,3,4]}`)

	if got := execute(t, s, "JSON.ARRTRIM", "doc", ".arr", "-2", "99"); got != ":2\r\n" {
		t.Fatal(got)
	}
	if got := execute(t, s, "JSON.GET", "doc", ".arr"); got != "$5\r\n[3,4]\r\n" {
		t.Fatal(got)
	}

	execute(t, s, "JSON.SET", "doc", "$", `{"arr":[1,2,3]}`)
	if got := execute(t, s, "JSON.ARRTRIM", "doc", ".arr", "9", "10"); got != ":0\r\n" {
		t.Fatal(got)
	}
	if got := execute(t, s, "JSON.GET", "doc", ".arr"); got != "$2\r\n[]\r\n" {
		t.Fatal(got)
	}
}

func TestJSONMergePreservesTTL(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "JSON.SET", "doc", "$", `{"obj":{"a":1}}`)
	execute(t, s, "PEXPIRE", "doc", "60000")
	execute(t, s, "JSON.MERGE", "doc", ".obj", `{"b":2}`)

	if got := execute(t, s, "PTTL", "doc"); strings.HasPrefix(got, ":-") {
		t.Fatalf("JSON.MERGE lost TTL: %q", got)
	}
}

func TestJSONMergeCreatesMissingRootKey(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "JSON.MERGE", "newdoc", "$", `{"a":1}`); got != "+OK\r\n" {
		t.Fatal(got)
	}
	if got := execute(t, s, "JSON.GET", "newdoc", "."); got != "$7\r\n{\"a\":1}\r\n" {
		t.Fatal(got)
	}
}


func TestJSONMSetAndForget(t *testing.T) {
	s := New(engine.New())

	if got := execute(
		t,
		s,
		"JSON.MSET",
		"doc1", "$", `{"a":1,"nested":{"x":1}}`,
		"doc2", "$", `{"b":2}`,
	); got != "+OK\r\n" {
		t.Fatal(got)
	}

	if got := execute(t, s, "JSON.MSET",
		"doc1", ".a", "3",
		"doc2", ".c", "4",
	); got != "+OK\r\n" {
		t.Fatal(got)
	}

	if got := execute(t, s, "JSON.GET", "doc1", "."); got != "$24\r\n{\"a\":3,\"nested\":{\"x\":1}}\r\n" {
		t.Fatal(got)
	}
	if got := execute(t, s, "JSON.GET", "doc2", "."); got != "$13\r\n{\"b\":2,\"c\":4}\r\n" {
		t.Fatal(got)
	}

	if got := execute(t, s, "JSON.FORGET", "doc1", ".nested.x"); got != ":1\r\n" {
		t.Fatal(got)
	}
	if got := execute(t, s, "JSON.GET", "doc1", "."); got != "$19\r\n{\"a\":3,\"nested\":{}}\r\n" {
		t.Fatal(got)
	}

	if got := execute(t, s, "JSON.FORGET", "doc2"); got != ":1\r\n" {
		t.Fatal(got)
	}
	if got := execute(t, s, "JSON.GET", "doc2"); got != "$-1\r\n" {
		t.Fatal(got)
	}
}

func TestJSONMSetRollsBackOnFailure(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "JSON.SET", "doc1", "$", `{"a":1}`)
	execute(t, s, "JSON.SET", "doc2", "$", `{"b":2}`)

	_, err := s.Execute([][]byte{
		[]byte("JSON.MSET"),
		[]byte("doc1"), []byte(".a"), []byte("9"),
		[]byte("doc2"), []byte(".missing.child"), []byte("7"),
	})
	if err == nil {
		t.Fatal("expected JSON.MSET failure")
	}

	if got := execute(t, s, "JSON.GET", "doc1", "."); got != "$7\r\n{\"a\":1}\r\n" {
		t.Fatalf("doc1 was partially mutated: %q", got)
	}
	if got := execute(t, s, "JSON.GET", "doc2", "."); got != "$7\r\n{\"b\":2}\r\n" {
		t.Fatalf("doc2 was partially mutated: %q", got)
	}
}

func TestJSONMSetRollsBackNewKeysOnInvalidJSON(t *testing.T) {
	s := New(engine.New())

	_, err := s.Execute([][]byte{
		[]byte("JSON.MSET"),
		[]byte("new1"), []byte("$"), []byte(`{"a":1}`),
		[]byte("new2"), []byte("$"), []byte("{invalid"),
	})
	if err == nil {
		t.Fatal("expected invalid JSON failure")
	}

	if got := execute(t, s, "JSON.GET", "new1"); got != "$-1\r\n" {
		t.Fatalf("new key survived rollback: %q", got)
	}
	if got := execute(t, s, "JSON.GET", "new2"); got != "$-1\r\n" {
		t.Fatalf("invalid key exists after rollback: %q", got)
	}
}

func TestJSONMSetPreservesExistingTTL(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "JSON.SET", "doc", "$", `{"a":1}`)
	execute(t, s, "PEXPIRE", "doc", "60000")

	execute(t, s, "JSON.MSET", "doc", ".a", "2")

	if got := execute(t, s, "PTTL", "doc"); strings.HasPrefix(got, ":-") {
		t.Fatalf("JSON.MSET lost TTL: %q", got)
	}
}


func TestJSONExactPathIntegration(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "JSON.SET", "doc", "$",
		`{"user":{"weird.key":"dot"},"items":[{"name":"a"},{"name":"b"},3]}`)

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"JSON.GET", "doc", "$.items[0].name"}, "$5\r\n[\"a\"]\r\n"},
		{[]string{"JSON.GET", "doc", "$.items[-1]"}, "$3\r\n[3]\r\n"},
		{[]string{"JSON.GET", "doc", `$["user"]["weird.key"]`}, "$7\r\n[\"dot\"]\r\n"},
		{[]string{"JSON.TYPE", "doc", "$.items[2]"}, "*1\r\n$7\r\ninteger\r\n"},
		{[]string{"JSON.SET", "doc", "$.items[1].name", `"Bee"`}, "+OK\r\n"},
		{[]string{"JSON.GET", "doc", "$.items[1].name"}, "$7\r\n[\"Bee\"]\r\n"},
		{[]string{"JSON.DEL", "doc", "$.items[0]"}, ":1\r\n"},
		{[]string{"JSON.GET", "doc", "$.items"}, "$20\r\n[[{\"name\":\"Bee\"},3]]\r\n"},
	} {
		if got := execute(t, s, tc.args...); got != tc.want {
			t.Fatalf("%q got %q want %q", tc.args, got, tc.want)
		}
	}
}

func TestJSONPathWildcardCoreCommands(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "JSON.SET", "doc", "$",
		`{"items":[{"score":1},{"score":2},{"score":3}],"obj":{"a":1,"b":2}}`)

	if got := execute(t, s, "JSON.GET", "doc", "$.items[*].score"); got != "$7\r\n[1,2,3]\r\n" {
		t.Fatal(got)
	}

	if got := execute(t, s, "JSON.TYPE", "doc", "$.items[*].score"); got != "*3\r\n$7\r\ninteger\r\n$7\r\ninteger\r\n$7\r\ninteger\r\n" {
		t.Fatal(got)
	}

	if got := execute(t, s, "JSON.SET", "doc", "$.items[*].score", "9"); got != "+OK\r\n" {
		t.Fatal(got)
	}
	if got := execute(t, s, "JSON.GET", "doc", "$.items[*].score"); got != "$7\r\n[9,9,9]\r\n" {
		t.Fatal(got)
	}

	if got := execute(t, s, "JSON.DEL", "doc", "$.obj.*"); got != ":2\r\n" {
		t.Fatal(got)
	}
	if got := execute(t, s, "JSON.GET", "doc", "$.obj"); got != "$4\r\n[{}]\r\n" {
		t.Fatal(got)
	}
}

func TestJSONLegacyPathRemainsScalar(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "JSON.SET", "doc", "$", `{"a":{"b":2}}`)

	if got := execute(t, s, "JSON.GET", "doc", ".a.b"); got != "$1\r\n2\r\n" {
		t.Fatal(got)
	}
	if got := execute(t, s, "JSON.TYPE", "doc", ".a.b"); got != "$7\r\ninteger\r\n" {
		t.Fatal(got)
	}
}

func TestJSONLegacyRootAliasDelete(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "JSON.SET", "doc", "$", `{"a":1}`)
	if got := execute(t, s, "JSON.DEL", "doc", "."); got != ":1\r\n" {
		t.Fatal(got)
	}
	if got := execute(t, s, "JSON.GET", "doc"); got != "$-1\r\n" {
		t.Fatal(got)
	}
}


func TestJSONPathRecursiveCoreCommands(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "JSON.SET", "doc", "$",
		`{"name":"root","nested":{"name":"child","deep":{"name":"leaf"}},"users":[{"id":1,"profile":{"id":10}},{"id":2}]}`)

	if got := execute(t, s, "JSON.GET", "doc", "$..name"); got != "$23\r\n[\"root\",\"child\",\"leaf\"]\r\n" {
		t.Fatal(got)
	}

	if got := execute(t, s, "JSON.GET", "doc", "$.users..id"); got != "$8\r\n[1,10,2]\r\n" {
		t.Fatal(got)
	}

	if got := execute(t, s, "JSON.TYPE", "doc", "$..name"); got != "*3\r\n$6\r\nstring\r\n$6\r\nstring\r\n$6\r\nstring\r\n" {
		t.Fatal(got)
	}

	if got := execute(t, s, "JSON.SET", "doc", "$..name", `"changed"`); got != "+OK\r\n" {
		t.Fatal(got)
	}
	if got := execute(t, s, "JSON.GET", "doc", "$..name"); got != "$31\r\n[\"changed\",\"changed\",\"changed\"]\r\n" {
		t.Fatal(got)
	}

	if got := execute(t, s, "JSON.DEL", "doc", "$.users..id"); got != ":3\r\n" {
		t.Fatal(got)
	}
	if got := execute(t, s, "JSON.GET", "doc", "$.users..id"); got != "$2\r\n[]\r\n" {
		t.Fatal(got)
	}
}


func TestJSONPathSliceAndUnionCoreCommands(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "JSON.SET", "doc", "$",
		`{"items":[0,1,2,3,4,5]}`)

	if got := execute(t, s, "JSON.GET", "doc", "$.items[1:4]"); got != "$7\r\n[1,2,3]\r\n" {
		t.Fatal(got)
	}
	if got := execute(t, s, "JSON.GET", "doc", "$.items[::2]"); got != "$7\r\n[0,2,4]\r\n" {
		t.Fatal(got)
	}
	if got := execute(t, s, "JSON.GET", "doc", "$.items[0,2,4]"); got != "$7\r\n[0,2,4]\r\n" {
		t.Fatal(got)
	}

	if got := execute(t, s, "JSON.SET", "doc", "$.items[1:5:2]", "9"); got != "+OK\r\n" {
		t.Fatal(got)
	}
	if got := execute(t, s, "JSON.GET", "doc", "$.items[*]"); got != "$13\r\n[0,9,2,9,4,5]\r\n" {
		t.Fatal(got)
	}

	if got := execute(t, s, "JSON.DEL", "doc", "$.items[0,2,4]"); got != ":3\r\n" {
		t.Fatal(got)
	}
	if got := execute(t, s, "JSON.GET", "doc", "$.items[*]"); got != "$7\r\n[9,9,5]\r\n" {
		t.Fatal(got)
	}
}


func TestJSONPathFilterCoreCommands(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "JSON.SET", "doc", "$",
		`{"items":[{"price":50,"name":"cheap"},{"price":100,"name":"mid"},{"price":150,"name":"expensive"}]}`)

	if got := execute(t, s, "JSON.GET", "doc", "$.items[?(@.price < 100)].name"); got != "$9\r\n[\"cheap\"]\r\n" {
		t.Fatal(got)
	}

	if got := execute(t, s, "JSON.TYPE", "doc", "$.items[?(@.price >= 100)].price"); got != "*2\r\n$7\r\ninteger\r\n$7\r\ninteger\r\n" {
		t.Fatal(got)
	}

	if got := execute(t, s, "JSON.SET", "doc", "$.items[?(@.price >= 100)].price", "999"); got != "+OK\r\n" {
		t.Fatal(got)
	}
	if got := execute(t, s, "JSON.GET", "doc", "$.items[*].price"); got != "$12\r\n[50,999,999]\r\n" {
		t.Fatal(got)
	}

	if got := execute(t, s, "JSON.DEL", "doc", "$.items[?(@.price == 999)]"); got != ":2\r\n" {
		t.Fatal(got)
	}
	if got := execute(t, s, "JSON.GET", "doc", "$.items[*].name"); got != "$9\r\n[\"cheap\"]\r\n" {
		t.Fatal(got)
	}
}
