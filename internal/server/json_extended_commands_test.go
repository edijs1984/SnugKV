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
		{[]string{"JSON.STRLEN", "doc", "$.name"}, ":5\r\n"},
		{[]string{"JSON.ARRLEN", "doc", "$.items"}, ":3\r\n"},
		{[]string{"JSON.OBJLEN", "doc", "$.user"}, ":2\r\n"},
		{[]string{"JSON.STRLEN", "doc", "$.count"}, "$-1\r\n"},
		{[]string{"JSON.ARRLEN", "doc", "$.flag"}, "$-1\r\n"},
		{[]string{"JSON.OBJLEN", "doc", "$.items"}, "$-1\r\n"},
		{[]string{"JSON.STRLEN", "doc", "$.missing"}, "$-1\r\n"},
		{[]string{"JSON.NUMINCRBY", "doc", "$.count", "3"}, "$1\r\n5\r\n"},
		{[]string{"JSON.NUMINCRBY", "doc", "$.price", "0.25"}, "$4\r\n1.75\r\n"},
		{[]string{"JSON.GET", "doc", "$.count"}, "$1\r\n5\r\n"},
		{[]string{"JSON.GET", "doc", "$.price"}, "$4\r\n1.75\r\n"},
		{[]string{"JSON.NUMINCRBY", "doc", "$.name", "1"}, "$-1\r\n"},
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
	execute(t, s, "JSON.NUMINCRBY", "doc", "$.count", "2")

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
			[]byte("$.count"),
			[]byte(increment),
		})
		if err == nil {
			t.Fatalf("accepted increment %q", increment)
		}
	}

	if got := execute(t, s, "JSON.GET", "doc", "$.count"); got != "$1\r\n1\r\n" {
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
		{[]string{"JSON.STRAPPEND", "doc", "$.name", `"dijs"`}, ":5\r\n"},
		{[]string{"JSON.GET", "doc", "$.name"}, "$7\r\n\"Edijs\"\r\n"},
		{[]string{"JSON.ARRAPPEND", "doc", "$.items", "2", `{"x":1}`}, ":3\r\n"},
		{[]string{"JSON.GET", "doc", "$.items"}, "$13\r\n[1,2,{\"x\":1}]\r\n"},
		{[]string{"JSON.OBJKEYS", "doc", "$.user"}, "*2\r\n$1\r\na\r\n$1\r\nb\r\n"},
		{[]string{"JSON.TOGGLE", "doc", "$.flag"}, ":0\r\n"},
		{[]string{"JSON.GET", "doc", "$.flag"}, "$5\r\nfalse\r\n"},
		{[]string{"JSON.TOGGLE", "doc", "$.flag"}, ":1\r\n"},
		{[]string{"JSON.GET", "doc", "$.flag"}, "$4\r\ntrue\r\n"},
		{[]string{"JSON.ARRAPPEND", "doc", "$.name", "1"}, "$-1\r\n"},
		{[]string{"JSON.STRAPPEND", "doc", "$.items", `"x"`}, "$-1\r\n"},
		{[]string{"JSON.OBJKEYS", "doc", "$.items"}, "$-1\r\n"},
		{[]string{"JSON.TOGGLE", "doc", "$.name"}, "$-1\r\n"},
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

	execute(t, s, "JSON.STRAPPEND", "doc", "$.name", `"b"`)
	execute(t, s, "JSON.ARRAPPEND", "doc", "$.items", "1")
	execute(t, s, "JSON.TOGGLE", "doc", "$.flag")

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
			[]byte("$.name"),
			[]byte(value),
		})
		if err == nil {
			t.Fatalf("accepted non-string append value %q", value)
		}
	}

	if got := execute(t, s, "JSON.GET", "doc", "$.name"); got != "$3\r\n\"a\"\r\n" {
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
		{[]string{"JSON.ARRINDEX", "doc", "$.arr", "2"}, ":1\r\n"},
		{[]string{"JSON.ARRINDEX", "doc", "$.arr", "9"}, ":-1\r\n"},
		{[]string{"JSON.ARRINDEX", "doc", "$.arr", "3", "0", "1"}, ":-1\r\n"},
		{[]string{"JSON.ARRINSERT", "doc", "$.arr", "1", "9", "8"}, ":5\r\n"},
		{[]string{"JSON.GET", "doc", "$.arr"}, "$11\r\n[1,9,8,2,3]\r\n"},
		{[]string{"JSON.ARRPOP", "doc", "$.arr"}, "$1\r\n3\r\n"},
		{[]string{"JSON.ARRPOP", "doc", "$.arr", "1"}, "$1\r\n9\r\n"},
		{[]string{"JSON.GET", "doc", "$.arr"}, "$7\r\n[1,8,2]\r\n"},
		{[]string{"JSON.CLEAR", "doc", "$.arr"}, ":1\r\n"},
		{[]string{"JSON.GET", "doc", "$.arr"}, "$2\r\n[]\r\n"},
		{[]string{"JSON.CLEAR", "doc", "$.obj"}, ":1\r\n"},
		{[]string{"JSON.GET", "doc", "$.obj"}, "$2\r\n{}\r\n"},
		{[]string{"JSON.CLEAR", "doc", "$.num"}, ":1\r\n"},
		{[]string{"JSON.GET", "doc", "$.num"}, "$1\r\n0\r\n"},
		{[]string{"JSON.CLEAR", "doc", "$.name"}, ":0\r\n"},
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

	execute(t, s, "JSON.ARRINSERT", "doc", "$.arr", "1", "9")
	execute(t, s, "JSON.ARRPOP", "doc", "$.arr", "0")
	execute(t, s, "JSON.CLEAR", "doc", "$.obj")

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
		[]byte("$.arr"),
		[]byte("99"),
		[]byte("3"),
	})
	if err == nil {
		t.Fatal("expected out-of-bounds error")
	}

	if got := execute(t, s, "JSON.GET", "doc", "$.arr"); got != "$5\r\n[1,2]\r\n" {
		t.Fatalf("out-of-bounds insert mutated value: %q", got)
	}
}
