package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestHashNumericCommands(t *testing.T) {
	s := New(engine.New())

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"HINCRBY", "h", "count", "5"}, ":5\r\n"},
		{[]string{"HINCRBY", "h", "count", "-2"}, ":3\r\n"},
		{[]string{"HINCRBYFLOAT", "h", "score", "1.25"}, "$4\r\n1.25\r\n"},
		{[]string{"HINCRBYFLOAT", "h", "score", "0.75"}, "$1\r\n2\r\n"},
		{[]string{"HGET", "h", "count"}, "$1\r\n3\r\n"},
		{[]string{"HGET", "h", "score"}, "$1\r\n2\r\n"},
	} {
		if got := execute(t, s, tc.args...); got != tc.want {
			t.Fatalf("%q got %q want %q", tc.args, got, tc.want)
		}
	}

	execute(t, s, "PEXPIRE", "h", "60000")
	if got := execute(t, s, "HINCRBY", "h", "count", "1"); got != ":4\r\n" {
		t.Fatal(got)
	}
	if ttl := execute(t, s, "PTTL", "h"); ttl == ":-1\r\n" || ttl == ":-2\r\n" {
		t.Fatalf("HINCRBY lost TTL: %q", ttl)
	}

	execute(t, s, "HSET", "bad", "i", "nope", "f", "NaN")
	if _, err := s.Execute([][]byte{[]byte("HINCRBY"), []byte("bad"), []byte("i"), []byte("1")}); err == nil || !strings.Contains(err.Error(), "integer") {
		t.Fatalf("invalid integer error=%v", err)
	}
	if _, err := s.Execute([][]byte{[]byte("HINCRBYFLOAT"), []byte("bad"), []byte("f"), []byte("1")}); err == nil || !strings.Contains(err.Error(), "float") {
		t.Fatalf("invalid float error=%v", err)
	}

	execute(t, s, "HSET", "max", "n", "9223372036854775807")
	if _, err := s.Execute([][]byte{[]byte("HINCRBY"), []byte("max"), []byte("n"), []byte("1")}); err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("overflow error=%v", err)
	}
}

func TestHashScanCommand(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "HSET", "h", "aa", "1", "ab", "2", "ba", "3", "bb", "4")

	if got := execute(t, s, "HSCAN", "h", "0", "COUNT", "2"); got != "*2\r\n$1\r\n2\r\n*4\r\n$2\r\naa\r\n$1\r\n1\r\n$2\r\nab\r\n$1\r\n2\r\n" {
		t.Fatalf("first HSCAN got %q", got)
	}

	if got := execute(t, s, "HSCAN", "h", "2", "MATCH", "b*", "COUNT", "2"); got != "*2\r\n$1\r\n0\r\n*4\r\n$2\r\nba\r\n$1\r\n3\r\n$2\r\nbb\r\n$1\r\n4\r\n" {
		t.Fatalf("matched HSCAN got %q", got)
	}

	if got := execute(t, s, "HSCAN", "h", "0", "MATCH", "a*", "COUNT", "10", "NOVALUES"); got != "*2\r\n$1\r\n0\r\n*2\r\n$2\r\naa\r\n$2\r\nab\r\n" {
		t.Fatalf("NOVALUES HSCAN got %q", got)
	}

	if got := execute(t, s, "HSCAN", "missing", "0"); got != "*2\r\n$1\r\n0\r\n*0\r\n" {
		t.Fatalf("missing HSCAN got %q", got)
	}

	for _, args := range [][][]byte{
		{[]byte("HSCAN"), []byte("h"), []byte("bad")},
		{[]byte("HSCAN"), []byte("h"), []byte("0"), []byte("COUNT"), []byte("0")},
		{[]byte("HINCRBY"), []byte("h"), []byte("count"), []byte("not-an-int")},
		{[]byte("HINCRBYFLOAT"), []byte("h"), []byte("score"), []byte("inf")},
	} {
		if _, err := s.Execute(args); err == nil {
			t.Fatalf("accepted invalid args %q", args)
		}
	}
}

func TestHashNumericAndScanWrongType(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "plain", "value")

	for _, args := range [][][]byte{
		{[]byte("HINCRBY"), []byte("plain"), []byte("n"), []byte("1")},
		{[]byte("HINCRBYFLOAT"), []byte("plain"), []byte("n"), []byte("1.5")},
		{[]byte("HSCAN"), []byte("plain"), []byte("0")},
	} {
		if _, err := s.Execute(args); err == nil || !strings.Contains(err.Error(), "WRONGTYPE") {
			t.Fatalf("%q error=%v, want WRONGTYPE", args, err)
		}
	}

	if !strings.Contains(execute(t, s, "COMMAND"), "hincrbyfloat") || !strings.Contains(execute(t, s, "COMMAND"), "hscan") {
		t.Fatal("HASH numeric/scan commands missing from COMMAND metadata")
	}
}
