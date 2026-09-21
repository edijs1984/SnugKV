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
