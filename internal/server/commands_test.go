package server

import (
	"errors"
	"fmt"
	"morphcache/internal/engine"
	"morphcache/internal/persistence"
	"strings"
	"testing"
)

func execute(t *testing.T, s *Server, args ...string) string {
	t.Helper()
	a := make([][]byte, len(args))
	for i, v := range args {
		a[i] = []byte(v)
	}
	out, err := s.Execute(a)
	if err != nil {
		t.Fatalf("%q: %v", args, err)
	}
	return string(out)
}
func TestCommandSubset(t *testing.T) {
	s := New(engine.New())
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"SET", "k", "v", "NX", "PX", "10000"}, "+OK\r\n"},
		{[]string{"SET", "k", "other", "NX"}, "$-1\r\n"},
		{[]string{"GET", "k"}, "$1\r\nv\r\n"},
		{[]string{"SET", "absent", "v", "XX"}, "$-1\r\n"},
		{[]string{"PERSIST", "k"}, ":1\r\n"}, {[]string{"PERSIST", "k"}, ":0\r\n"},
		{[]string{"TTL", "k"}, ":-1\r\n"}, {[]string{"EXISTS", "k", "k", "absent"}, ":2\r\n"},
		{[]string{"GETSET", "k", "long"}, "$1\r\nv\r\n"}, {[]string{"STRLEN", "k"}, ":4\r\n"},
		{[]string{"MSET", "a", "1", "b", "2"}, "+OK\r\n"},
		{[]string{"MGET", "b", "none", "a"}, "*3\r\n$1\r\n2\r\n$-1\r\n$1\r\n1\r\n"},
		{[]string{"INCRBY", "a", "5"}, ":6\r\n"}, {[]string{"DECR", "a"}, ":5\r\n"},
		{[]string{"DECRBY", "a", "6"}, ":-1\r\n"},
		{[]string{"DECRBY", "a", "-9223372036854775808"}, ":9223372036854775807\r\n"},
		{[]string{"EXPIRE", "k", "0"}, ":1\r\n"}, {[]string{"GET", "k"}, "$-1\r\n"},
		{[]string{"PEXPIRE", "absent", "100"}, ":0\r\n"},
		{[]string{"SETNX", "a", "x"}, ":0\r\n"}, {[]string{"SETNX", "new", "x"}, ":1\r\n"},
		{[]string{"DEL", "a", "a", "new"}, ":2\r\n"}, {[]string{"DBSIZE"}, ":1\r\n"},
		{[]string{"SELECT", "0"}, "+OK\r\n"}, {[]string{"QUIT"}, "+OK\r\n"},
	} {
		if got := execute(t, s, tc.args...); got != tc.want {
			t.Fatalf("%q got %q want %q", tc.args, got, tc.want)
		}
	}
	if !strings.Contains(execute(t, s, "COMMAND"), "mset") {
		t.Fatal("command metadata")
	}
	if !strings.Contains(execute(t, s, "HELLO", "2"), "morphcache") {
		t.Fatal("hello")
	}
}
func TestRejectedOptionsDoNotMutate(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "k", "original", "PX", "60000")
	for _, options := range [][]string{{"PX"}, {"EX", "0"}, {"PX", "-1"}, {"PX", "9223372036854775807"}, {"EX", "1", "PX", "2"}, {"NX", "XX"}, {"NX", "NX"}, {"INVALID"}, {"PX", " 1"}} {
		args := [][]byte{[]byte("SET"), []byte("k"), []byte("new")}
		for _, o := range options {
			args = append(args, []byte(o))
		}
		if _, err := s.Execute(args); err == nil {
			t.Fatalf("accepted %q", options)
		}
		if got := execute(t, s, "GET", "k"); got != "$8\r\noriginal\r\n" {
			t.Fatal(got)
		}
		if got := execute(t, s, "PTTL", "k"); strings.HasPrefix(got, ":-") {
			t.Fatal("lost TTL")
		}
	}
	for _, args := range [][]string{{"PING", "a", "b"}, {"INCRBY", "k", "+1"}, {"SELECT", "1"}, {"HELLO", "3"}, {"MSET", "k", "x", "missing"}, {"EXPIRE", "k", "9223372036854775807"}} {
		a := make([][]byte, len(args))
		for i, v := range args {
			a[i] = []byte(v)
		}
		if _, err := s.Execute(a); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}

type failingJournal struct{}

func (failingJournal) Append([]persistence.Record) error { return errors.New("disk full") }
func TestDurabilityRollback(t *testing.T) {
	store := engine.New()
	store.Set("k", []byte("old"), 60000)
	s := New(store)
	s.SetJournal(failingJournal{})
	if _, err := s.Execute([][]byte{[]byte("SET"), []byte("k"), []byte("new")}); err == nil {
		t.Fatal("append failure ignored")
	}
	if got := execute(t, s, "GET", "k"); got != "$3\r\nold\r\n" {
		t.Fatal("failed write visible")
	}
	if store.TTL("k", true) < 0 {
		t.Fatal("rollback lost TTL")
	}
}

func TestEvictionAllowsBoundedWrite(t *testing.T) {
	store, err := engine.NewWithOptions(engine.Options{Shards: 1, MaxMemory: 100000})
	if err != nil {
		t.Fatal(err)
	}
	s := New(store)
	s.eviction = "allkeys-lru"
	for i := 0; i < 10; i++ {
		value := strings.Repeat("x", 10000)
		execute(t, s, "SET", fmt.Sprint(i), value)
	}
	if store.Memory().AccountedBytes > 100000 {
		t.Fatal("memory cap exceeded")
	}
	if got := store.Stats().Keys; got >= 10 || got == 0 {
		t.Fatalf("unexpected keys %d", got)
	}
}
