package server

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func parseBulkArray(t *testing.T, raw string) []string {
	t.Helper()
	if !strings.HasPrefix(raw, "*") {
		t.Fatalf("not an array: %q", raw)
	}
	lineEnd := strings.Index(raw, "\r\n")
	if lineEnd < 0 {
		t.Fatalf("invalid RESP array: %q", raw)
	}
	count, err := strconv.Atoi(raw[1:lineEnd])
	if err != nil {
		t.Fatal(err)
	}
	pos := lineEnd + 2
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		if pos >= len(raw) || raw[pos] != '$' {
			t.Fatalf("expected bulk at item %d in %q", i, raw)
		}
		lenEnd := strings.Index(raw[pos:], "\r\n")
		if lenEnd < 0 {
			t.Fatalf("invalid bulk header in %q", raw)
		}
		lenEnd += pos
		n, err := strconv.Atoi(raw[pos+1 : lenEnd])
		if err != nil || n < 0 {
			t.Fatalf("invalid bulk length in %q", raw)
		}
		start := lenEnd + 2
		end := start + n
		if end+2 > len(raw) || raw[end:end+2] != "\r\n" {
			t.Fatalf("invalid bulk body in %q", raw)
		}
		out = append(out, raw[start:end])
		pos = end + 2
	}
	return out
}

func TestHMSetCompatibility(t *testing.T) {
	s := New(engine.New())
	if got := execute(t, s, "HMSET", "h", "a", "1", "b", "2"); got != "+OK\r\n" {
		t.Fatalf("HMSET = %q", got)
	}
	if got := execute(t, s, "HMSET", "h", "a", "updated"); got != "+OK\r\n" {
		t.Fatalf("HMSET update = %q", got)
	}
	if got := execute(t, s, "HGET", "h", "a"); got != "$7\r\nupdated\r\n" {
		t.Fatalf("HGET = %q", got)
	}
}

func TestHRandFieldCompatibility(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "HSET", "h", "a", "1", "b", "2", "c", "3")

	single := execute(t, s, "HRANDFIELD", "h")
	if single != "$1\r\na\r\n" && single != "$1\r\nb\r\n" && single != "$1\r\nc\r\n" {
		t.Fatalf("unexpected single HRANDFIELD: %q", single)
	}

	positive := parseBulkArray(t, execute(t, s, "HRANDFIELD", "h", "10"))
	if len(positive) != 3 {
		t.Fatalf("positive count returned %d fields", len(positive))
	}
	seen := map[string]bool{}
	for _, field := range positive {
		if seen[field] {
			t.Fatalf("positive count repeated field %q", field)
		}
		seen[field] = true
	}

	negative := parseBulkArray(t, execute(t, s, "HRANDFIELD", "h", "-5"))
	if len(negative) != 5 {
		t.Fatalf("negative count returned %d fields", len(negative))
	}
	for _, field := range negative {
		if field != "a" && field != "b" && field != "c" {
			t.Fatalf("unexpected field %q", field)
		}
	}

	withValues := parseBulkArray(t, execute(t, s, "HRANDFIELD", "h", "3", "WITHVALUES"))
	if len(withValues) != 6 {
		t.Fatalf("WITHVALUES returned %d elements", len(withValues))
	}
	values := map[string]string{"a": "1", "b": "2", "c": "3"}
	for i := 0; i < len(withValues); i += 2 {
		if values[withValues[i]] != withValues[i+1] {
			t.Fatalf("bad field/value pair %q=%q", withValues[i], withValues[i+1])
		}
	}

	if got := execute(t, s, "HRANDFIELD", "missing"); got != "$-1\r\n" {
		t.Fatalf("missing HRANDFIELD = %q", got)
	}
	if got := execute(t, s, "HRANDFIELD", "missing", "2"); got != "*0\r\n" {
		t.Fatalf("missing counted HRANDFIELD = %q", got)
	}
	if got := execute(t, s, "HRANDFIELD", "h", "0"); got != "*0\r\n" {
		t.Fatalf("zero-count HRANDFIELD = %q", got)
	}
}

func TestHashRenamePreservesTypeAndTTL(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "HSET", "source", "a", "1", "b", "2")
	execute(t, s, "PEXPIRE", "source", "60000")

	if got := execute(t, s, "RENAME", "source", "target"); got != "+OK\r\n" {
		t.Fatalf("RENAME = %q", got)
	}
	if got := execute(t, s, "TYPE", "target"); got != "+hash\r\n" {
		t.Fatalf("TYPE target = %q", got)
	}
	if got := execute(t, s, "HGET", "target", "a"); got != "$1\r\n1\r\n" {
		t.Fatalf("renamed HGET = %q", got)
	}
	if got := execute(t, s, "TYPE", "source"); got != "+none\r\n" {
		t.Fatalf("TYPE source = %q", got)
	}
	pttl := execute(t, s, "PTTL", "target")
	if pttl == ":-1\r\n" || pttl == ":-2\r\n" {
		t.Fatalf("renamed HASH lost TTL: %q", pttl)
	}

	execute(t, s, "HSET", "nx-source", "f", "v")
	execute(t, s, "SET", "occupied", "string")
	if got := execute(t, s, "RENAMENX", "nx-source", "occupied"); got != ":0\r\n" {
		t.Fatalf("RENAMENX = %q", got)
	}
	if got := execute(t, s, "HGET", "nx-source", "f"); got != "$1\r\nv\r\n" {
		t.Fatalf("RENAMENX changed source: %q", got)
	}
}

func TestHashOOMMutationRollsBack(t *testing.T) {
	store, err := engine.NewWithOptions(engine.Options{Shards: 1, MaxMemory: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	s := New(store)
	if got := execute(t, s, "HSET", "h", "f", "small"); got != ":1\r\n" {
		t.Fatalf("initial HSET = %q", got)
	}

	large := strings.Repeat("x", 48<<10)
	_, err = s.Execute([][]byte{[]byte("HSET"), []byte("h"), []byte("f"), []byte(large)})
	if !errors.Is(err, engine.ErrOOM) {
		t.Fatalf("large HSET error = %v, want ErrOOM", err)
	}
	if got := execute(t, s, "HGET", "h", "f"); got != "$5\r\nsmall\r\n" {
		t.Fatalf("OOM mutated HASH: %q", got)
	}
}
