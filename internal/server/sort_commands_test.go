package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestSortNumericAlphaLimitAndSourceTypes(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "RPUSH", "nums", "3", "1", "2")
	if got := execute(t, s, "SORT", "nums"); got != "*3\r\n$1\r\n1\r\n$1\r\n2\r\n$1\r\n3\r\n" {
		t.Fatalf("SORT list = %q", got)
	}
	if got := execute(t, s, "SORT_RO", "nums", "DESC", "LIMIT", "1", "1"); got != "*1\r\n$1\r\n2\r\n" {
		t.Fatalf("SORT_RO DESC LIMIT = %q", got)
	}

	execute(t, s, "RPUSH", "names", "bob", "Alice", "carol")
	if got := execute(t, s, "SORT", "names", "ALPHA"); got != "*3\r\n$5\r\nAlice\r\n$3\r\nbob\r\n$5\r\ncarol\r\n" {
		t.Fatalf("SORT ALPHA = %q", got)
	}

	execute(t, s, "SADD", "setnums", "3", "1", "2")
	if got := execute(t, s, "SORT", "setnums"); got != "*3\r\n$1\r\n1\r\n$1\r\n2\r\n$1\r\n3\r\n" {
		t.Fatalf("SORT set = %q", got)
	}

	execute(t, s, "ZADD", "znums", "30", "3", "10", "1", "20", "2")
	if got := execute(t, s, "SORT", "znums"); got != "*3\r\n$1\r\n1\r\n$1\r\n2\r\n$1\r\n3\r\n" {
		t.Fatalf("SORT zset = %q", got)
	}
}

func TestSortByGetHashAndNoSort(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "RPUSH", "ids", "2", "1", "3")
	execute(t, s, "SET", "weight_1", "20")
	execute(t, s, "SET", "weight_2", "10")
	execute(t, s, "SET", "weight_3", "30")
	execute(t, s, "SET", "name_1", "one")
	execute(t, s, "SET", "name_2", "two")
	execute(t, s, "SET", "name_3", "three")

	if got := execute(t, s, "SORT", "ids", "BY", "weight_*"); got != "*3\r\n$1\r\n2\r\n$1\r\n1\r\n$1\r\n3\r\n" {
		t.Fatalf("SORT BY = %q", got)
	}
	want := "*6\r\n$1\r\n2\r\n$3\r\ntwo\r\n$1\r\n1\r\n$3\r\none\r\n$1\r\n3\r\n$5\r\nthree\r\n"
	if got := execute(t, s, "SORT", "ids", "BY", "weight_*", "GET", "#", "GET", "name_*"); got != want {
		t.Fatalf("SORT GET = %q", got)
	}

	execute(t, s, "HSET", "user:1", "score", "20", "name", "Alice")
	execute(t, s, "HSET", "user:2", "score", "10", "name", "Bob")
	execute(t, s, "HSET", "user:3", "score", "30", "name", "Carol")
	want = "*3\r\n$3\r\nBob\r\n$5\r\nAlice\r\n$5\r\nCarol\r\n"
	if got := execute(t, s, "SORT", "ids", "BY", "user:*->score", "GET", "user:*->name"); got != want {
		t.Fatalf("SORT hash dereference = %q", got)
	}

	if got := execute(t, s, "SORT", "ids", "BY", "nosort"); got != "*3\r\n$1\r\n2\r\n$1\r\n1\r\n$1\r\n3\r\n" {
		t.Fatalf("SORT BY nosort = %q", got)
	}
	if got := execute(t, s, "SORT", "ids", "BY", "nosort", "GET", "fixed-key"); got != "*3\r\n$-1\r\n$-1\r\n$-1\r\n" {
		t.Fatalf("SORT GET fixed pattern = %q", got)
	}
}

func TestSortStoreReplacementTTLAndMissingGet(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "RPUSH", "src", "3", "1", "2")
	execute(t, s, "SET", "dst", "old")
	execute(t, s, "EXPIRE", "dst", "600")

	if got := execute(t, s, "SORT", "src", "STORE", "dst"); got != ":3\r\n" {
		t.Fatalf("SORT STORE = %q", got)
	}
	if got := execute(t, s, "TYPE", "dst"); got != "+list\r\n" {
		t.Fatalf("STORE type = %q", got)
	}
	if got := execute(t, s, "TTL", "dst"); got != ":-1\r\n" {
		t.Fatalf("STORE TTL = %q", got)
	}
	if got := execute(t, s, "LRANGE", "dst", "0", "-1"); got != "*3\r\n$1\r\n1\r\n$1\r\n2\r\n$1\r\n3\r\n" {
		t.Fatalf("STORE list = %q", got)
	}

	execute(t, s, "RPUSH", "one", "1")
	if got := execute(t, s, "SORT", "one", "GET", "missing_*", "STORE", "missing-store"); got != ":1\r\n" {
		t.Fatalf("SORT missing GET STORE = %q", got)
	}
	if got := execute(t, s, "LRANGE", "missing-store", "0", "-1"); got != "*1\r\n$0\r\n\r\n" {
		t.Fatalf("missing GET stored value = %q", got)
	}

	execute(t, s, "SET", "empty-dst", "old")
	if got := execute(t, s, "SORT", "does-not-exist", "STORE", "empty-dst"); got != ":0\r\n" {
		t.Fatalf("empty SORT STORE = %q", got)
	}
	if got := execute(t, s, "EXISTS", "empty-dst"); got != ":0\r\n" {
		t.Fatalf("empty STORE destination still exists: %q", got)
	}
}

func TestSortErrorsAndReadOnlyStore(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "string-source", "123")
	if _, err := s.Execute([][]byte{[]byte("SORT"), []byte("string-source")}); err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE ") {
		t.Fatalf("SORT wrongtype error = %v", err)
	}

	execute(t, s, "RPUSH", "badnums", "1", "nope")
	if _, err := s.Execute([][]byte{[]byte("SORT"), []byte("badnums")}); err == nil || err.Error() != "ERR One or more scores can't be converted into double" {
		t.Fatalf("SORT numeric error = %v", err)
	}

	if _, err := s.Execute([][]byte{[]byte("SORT_RO"), []byte("badnums"), []byte("STORE"), []byte("dst")}); err == nil || err.Error() != "ERR syntax error" {
		t.Fatalf("SORT_RO STORE error = %v", err)
	}
	if _, err := s.Execute([][]byte{[]byte("SORT"), []byte("badnums"), []byte("LIMIT"), []byte("x"), []byte("1")}); err == nil || err.Error() != "ERR value is not an integer or out of range" {
		t.Fatalf("SORT LIMIT error = %v", err)
	}
}
