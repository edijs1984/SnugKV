package server

import (
	"bytes"
	"testing"

	"snugkv/internal/engine"
)

func execScanTest(t *testing.T, s *Server, args ...string) []byte {
	t.Helper()
	command := make([][]byte, len(args))
	for i := range args {
		command[i] = []byte(args[i])
	}
	out, err := s.Execute(command)
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return out
}

func TestScanTypeMatchAndWorkHintCompatibility(t *testing.T) {
	store := engine.New()
	s := New(store)

	execScanTest(t, s, "SET", "a:string", "v")
	execScanTest(t, s, "HSET", "b:hash", "f", "v")
	execScanTest(t, s, "SADD", "c:set", "v")
	execScanTest(t, s, "RPUSH", "d:list", "v")
	execScanTest(t, s, "ZADD", "e:zset", "1", "v")

	// COUNT is work, not a post-filter result limit. The first sorted key is a
	// string, so TYPE hash with COUNT 1 must return an empty page and cursor 1.
	out := execScanTest(t, s, "SCAN", "0", "COUNT", "1", "TYPE", "hash")
	want := "*2\r\n$1\r\n1\r\n*0\r\n"
	if string(out) != want {
		t.Fatalf("SCAN TYPE first page = %q, want %q", out, want)
	}

	out = execScanTest(t, s, "SCAN", "1", "COUNT", "1", "TYPE", "hash")
	want = "*2\r\n$1\r\n2\r\n*1\r\n$6\r\nb:hash\r\n"
	if string(out) != want {
		t.Fatalf("SCAN TYPE second page = %q, want %q", out, want)
	}

	out = execScanTest(t, s, "SCAN", "0", "MATCH", "[ab]:*", "COUNT", "5")
	if !bytes.Contains(out, []byte("a:string")) || !bytes.Contains(out, []byte("b:hash")) || bytes.Contains(out, []byte("c:set")) {
		t.Fatalf("SCAN class MATCH response = %q", out)
	}

	out = execScanTest(t, s, "SCAN", "0", "MATCH", "", "COUNT", "5")
	if !bytes.HasSuffix(out, []byte("*0\r\n")) {
		t.Fatalf("SCAN MATCH empty should return no keys: %q", out)
	}
}

func TestAggregateScansPreserveEmptyMatchAndSourceCursor(t *testing.T) {
	store := engine.New()
	s := New(store)

	execScanTest(t, s, "HSET", "h", "aa", "1", "bb", "2")
	execScanTest(t, s, "SADD", "set", "aa", "bb")
	execScanTest(t, s, "ZADD", "z", "1", "aa", "2", "bb")

	for _, tc := range []struct {
		name string
		cmd  []string
	}{
		{"HSCAN", []string{"HSCAN", "h", "0", "MATCH", "", "COUNT", "1"}},
		{"SSCAN", []string{"SSCAN", "set", "0", "MATCH", "", "COUNT", "1"}},
		{"ZSCAN", []string{"ZSCAN", "z", "0", "MATCH", "", "COUNT", "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := execScanTest(t, s, tc.cmd...)
			// One source item was inspected, no item matched, and work remains.
			want := "*2\r\n$1\r\n1\r\n*0\r\n"
			if string(out) != want {
				t.Fatalf("%s = %q, want %q", tc.name, out, want)
			}
		})
	}
}

func TestScanGlobEscapingAndBinaryMember(t *testing.T) {
	store := engine.New()
	s := New(store)

	execScanTest(t, s, "SADD", "s", "a*b", "axb", "a?b")
	out := execScanTest(t, s, "SSCAN", "s", "0", "MATCH", `a\*b`, "COUNT", "10")
	if !bytes.Contains(out, []byte("a*b")) || bytes.Contains(out, []byte("axb")) {
		t.Fatalf("escaped star response = %q", out)
	}

	// Exercise the engine directly for bytes that cannot be represented safely
	// as a convenient Go source string command fixture.
	if _, err := store.SetAdd("binary", [][]byte{{'x', 0x00, 0xff}}); err != nil {
		t.Fatal(err)
	}
	next, members, err := store.SetScan("binary", 0, 10, []byte{'x', '?', '?'})
	if err != nil || next != 0 || len(members) != 1 || !bytes.Equal(members[0], []byte{'x', 0x00, 0xff}) {
		t.Fatalf("binary SSCAN next=%d members=%q err=%v", next, members, err)
	}
}
