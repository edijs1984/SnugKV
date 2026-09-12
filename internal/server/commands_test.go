package server

import (
	"errors"
	"fmt"
	"snugkv/internal/engine"
	"snugkv/internal/persistence"
	"strings"
	"testing"
	"time"
	"strconv"
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
		{[]string{"TYPE", "k"}, "+string\r\n"},
        {[]string{"TYPE", "missing"}, "+none\r\n"},
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
	if !strings.Contains(execute(t, s, "HELLO", "2"), "snugkv") {
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
func TestTypeExpiredKey(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "SET", "temp", "value", "PX", "1")

	time.Sleep(5 * time.Millisecond)

	if got := execute(t, s, "TYPE", "temp"); got != "+none\r\n" {
		t.Fatalf("got %q want %q", got, "+none\r\n")
	}
}
func TestScan(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "SET", "user:1", "one")
	execute(t, s, "SET", "user:2", "two")
	execute(t, s, "SET", "session:1", "three")

	got := execute(t, s, "SCAN", "0", "COUNT", "100")

	if !strings.Contains(got, "user:1") {
		t.Fatalf("SCAN missing user:1: %q", got)
	}

	if !strings.Contains(got, "user:2") {
		t.Fatalf("SCAN missing user:2: %q", got)
	}

	if !strings.Contains(got, "session:1") {
		t.Fatalf("SCAN missing session:1: %q", got)
	}
}

func TestScanMatch(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "SET", "user:1", "one")
	execute(t, s, "SET", "user:2", "two")
	execute(t, s, "SET", "session:1", "three")

	got := execute(
		t,
		s,
		"SCAN",
		"0",
		"MATCH",
		"user:*",
		"COUNT",
		"100",
	)

	if !strings.Contains(got, "user:1") {
		t.Fatalf("SCAN missing user:1: %q", got)
	}

	if !strings.Contains(got, "user:2") {
		t.Fatalf("SCAN missing user:2: %q", got)
	}

	if strings.Contains(got, "session:1") {
		t.Fatalf("SCAN returned unwanted key: %q", got)
	}
}

func TestKeys(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "SET", "user:1", "one")
	execute(t, s, "SET", "user:2", "two")
	execute(t, s, "SET", "session:1", "three")

	got := execute(t, s, "KEYS", "user:*")

	if !strings.Contains(got, "user:1") {
		t.Fatalf("KEYS missing user:1: %q", got)
	}

	if !strings.Contains(got, "user:2") {
		t.Fatalf("KEYS missing user:2: %q", got)
	}

	if strings.Contains(got, "session:1") {
		t.Fatalf("KEYS returned unwanted key: %q", got)
	}
}

func TestKeysAll(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "SET", "a", "1")
	execute(t, s, "SET", "b", "2")

	got := execute(t, s, "KEYS", "*")

	if !strings.Contains(got, "a") || !strings.Contains(got, "b") {
		t.Fatalf("KEYS * missing keys: %q", got)
	}
}

func TestRandomKey(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "RANDOMKEY"); got != "$-1\r\n" {
		t.Fatalf("empty RANDOMKEY got %q", got)
	}

	execute(t, s, "SET", "only-key", "value")

	if got := execute(t, s, "RANDOMKEY"); got != "$8\r\nonly-key\r\n" {
		t.Fatalf("RANDOMKEY got %q", got)
	}
}
func TestRename(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "SET", "old", "hello")

	if got := execute(t, s, "RENAME", "old", "new"); got != "+OK\r\n" {
		t.Fatalf("RENAME got %q", got)
	}

	if got := execute(t, s, "GET", "old"); got != "$-1\r\n" {
		t.Fatalf("old key still exists: %q", got)
	}

	if got := execute(t, s, "GET", "new"); got != "$5\r\nhello\r\n" {
		t.Fatalf("new key incorrect: %q", got)
	}
}

func TestRenameNX(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "SET", "source", "one")
	execute(t, s, "SET", "destination", "two")

	if got := execute(
		t,
		s,
		"RENAMENX",
		"source",
		"destination",
	); got != ":0\r\n" {
		t.Fatalf("RENAMENX existing destination got %q", got)
	}

	if got := execute(t, s, "GET", "source"); got != "$3\r\none\r\n" {
		t.Fatalf("source was modified: %q", got)
	}

	execute(t, s, "DEL", "destination")

	if got := execute(
		t,
		s,
		"RENAMENX",
		"source",
		"destination",
	); got != ":1\r\n" {
		t.Fatalf("RENAMENX got %q", got)
	}

	if got := execute(t, s, "GET", "destination"); got != "$3\r\none\r\n" {
		t.Fatalf("destination incorrect: %q", got)
	}
}

func TestRenamePreservesTTL(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "SET", "old", "hello", "PX", "60000")
	execute(t, s, "RENAME", "old", "new")

	got := execute(t, s, "PTTL", "new")

	if got == ":-1\r\n" || got == ":-2\r\n" {
		t.Fatalf("RENAME lost TTL: %q", got)
	}
}

func TestFlushDB(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "SET", "a", "1")
	execute(t, s, "SET", "b", "2")
	execute(t, s, "SET", "c", "3")

	if got := execute(t, s, "DBSIZE"); got != ":3\r\n" {
		t.Fatalf("before FLUSHDB got %q", got)
	}

	if got := execute(t, s, "FLUSHDB"); got != "+OK\r\n" {
		t.Fatalf("FLUSHDB got %q", got)
	}

	if got := execute(t, s, "DBSIZE"); got != ":0\r\n" {
		t.Fatalf("after FLUSHDB got %q", got)
	}

	if got := execute(t, s, "GET", "a"); got != "$-1\r\n" {
		t.Fatalf("key survived FLUSHDB: %q", got)
	}
}

func TestFlushDBDurabilityRollback(t *testing.T) {
	store := engine.New()

	store.Set("a", []byte("one"), 0)
	store.Set("b", []byte("two"), 0)

	s := New(store)
	s.SetJournal(failingJournal{})

	if _, err := s.Execute([][]byte{
		[]byte("FLUSHDB"),
	}); err == nil {
		t.Fatal("expected FLUSHDB persistence failure")
	}

	if got := execute(t, s, "GET", "a"); got != "$3\r\none\r\n" {
		t.Fatalf("rollback lost a: %q", got)
	}

	if got := execute(t, s, "GET", "b"); got != "$3\r\ntwo\r\n" {
		t.Fatalf("rollback lost b: %q", got)
	}
}
func TestUnlink(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "SET", "a", "1")
	execute(t, s, "SET", "b", "2")

	if got := execute(t, s, "UNLINK", "a", "b"); got != ":2\r\n" {
		t.Fatalf("UNLINK got %q", got)
	}

	if got := execute(t, s, "GET", "a"); got != "$-1\r\n" {
		t.Fatalf("a still exists: %q", got)
	}

	if got := execute(t, s, "GET", "b"); got != "$-1\r\n" {
		t.Fatalf("b still exists: %q", got)
	}
}

func TestTouch(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "SET", "a", "1")
	execute(t, s, "SET", "b", "2")

	if got := execute(t, s, "TOUCH", "a", "b", "missing"); got != ":2\r\n" {
		t.Fatalf("TOUCH got %q", got)
	}

	if got := execute(t, s, "GET", "a"); got != "$1\r\n1\r\n" {
		t.Fatalf("TOUCH modified a: %q", got)
	}
}

func TestIncrByFloat(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "INCRBYFLOAT", "balance", "1.5"); got != "$3\r\n1.5\r\n" {
		t.Fatalf("first INCRBYFLOAT got %q", got)
	}

	if got := execute(t, s, "INCRBYFLOAT", "balance", "2.25"); got != "$4\r\n3.75\r\n" {
		t.Fatalf("second INCRBYFLOAT got %q", got)
	}

	if got := execute(t, s, "GET", "balance"); got != "$4\r\n3.75\r\n" {
		t.Fatalf("stored value got %q", got)
	}
}

func TestIncrByFloatNegative(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "SET", "n", "10")

	if got := execute(t, s, "INCRBYFLOAT", "n", "-2.5"); got != "$3\r\n7.5\r\n" {
		t.Fatalf("INCRBYFLOAT got %q", got)
	}
}
func TestIncrByFloatRejectsInvalidValue(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "SET", "n", "hello")

	_, err := s.Execute([][]byte{
		[]byte("INCRBYFLOAT"),
		[]byte("n"),
		[]byte("1.5"),
	})

	if err == nil {
		t.Fatal("expected INCRBYFLOAT error")
	}
}

func TestExpireAt(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "SET", "a", "1")

	future := time.Now().Add(time.Hour).Unix()

	if got := execute(
		t,
		s,
		"EXPIREAT",
		"a",
		strconv.FormatInt(future, 10),
	); got != ":1\r\n" {
		t.Fatalf("EXPIREAT got %q", got)
	}

	if got := execute(t, s, "TTL", "a"); got == ":-1\r\n" {
		t.Fatalf("EXPIREAT did not set TTL")
	}
}


func TestExpireTime(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "SET", "a", "1")

	future := time.Now().Add(time.Hour).Unix()

	execute(
		t,
		s,
		"EXPIREAT",
		"a",
		strconv.FormatInt(future, 10),
	)

	got := execute(t, s, "EXPIRETIME", "a")

	expected := ":" + strconv.FormatInt(future, 10) + "\r\n"

	if got != expected {
		t.Fatalf("EXPIRETIME got %q want %q", got, expected)
	}
}

func TestExpireTimeSpecialValues(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "EXPIRETIME", "missing"); got != ":-2\r\n" {
		t.Fatalf("missing EXPIRETIME got %q", got)
	}

	execute(t, s, "SET", "persistent", "1")

	if got := execute(t, s, "EXPIRETIME", "persistent"); got != ":-1\r\n" {
		t.Fatalf("persistent EXPIRETIME got %q", got)
	}
}

func TestGetDel(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "SET", "a", "hello")

	if got := execute(t, s, "GETDEL", "a"); got != "$5\r\nhello\r\n" {
		t.Fatalf("GETDEL got %q", got)
	}

	if got := execute(t, s, "GET", "a"); got != "$-1\r\n" {
		t.Fatalf("GETDEL did not delete key: %q", got)
	}

	if got := execute(t, s, "GETDEL", "missing"); got != "$-1\r\n" {
		t.Fatalf("missing GETDEL got %q", got)
	}
}

func TestGetEx(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "SET", "a", "hello")

	if got := execute(t, s, "GETEX", "a", "EX", "60"); got != "$5\r\nhello\r\n" {
		t.Fatalf("GETEX got %q", got)
	}

	ttl := execute(t, s, "TTL", "a")

	if ttl == ":-1\r\n" || ttl == ":-2\r\n" {
		t.Fatalf("GETEX did not set TTL: %q", ttl)
	}
}

func TestGetExPersist(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "SET", "a", "hello", "EX", "60")

	if got := execute(t, s, "GETEX", "a", "PERSIST"); got != "$5\r\nhello\r\n" {
		t.Fatalf("GETEX PERSIST got %q", got)
	}

	if got := execute(t, s, "TTL", "a"); got != ":-1\r\n" {
		t.Fatalf("GETEX PERSIST did not remove TTL: %q", got)
	}
}

func TestGetExMissing(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "GETEX", "missing", "EX", "60"); got != "$-1\r\n" {
		t.Fatalf("GETEX missing got %q", got)
	}
}