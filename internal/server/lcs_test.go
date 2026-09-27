package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func newLCSTestServer(t *testing.T) *Server {
	t.Helper()

	serv := New(engine.New())
	if _, err := serv.Execute([][]byte{[]byte("MSET"), []byte("key1"), []byte("ohmytext"), []byte("key2"), []byte("mynewtext")}); err != nil {
		t.Fatalf("MSET failed: %v", err)
	}
	return serv
}

func TestLCSBasicAndLength(t *testing.T) {
	serv := newLCSTestServer(t)

	reply, err := serv.Execute([][]byte{[]byte("LCS"), []byte("key1"), []byte("key2")})
	if err != nil {
		t.Fatalf("LCS failed: %v", err)
	}
	if got, want := string(reply), "$6\r\nmytext\r\n"; got != want {
		t.Fatalf("LCS reply=%q want=%q", got, want)
	}

	reply, err = serv.Execute([][]byte{[]byte("LCS"), []byte("key1"), []byte("key2"), []byte("LEN")})
	if err != nil {
		t.Fatalf("LCS LEN failed: %v", err)
	}
	if got, want := string(reply), ":6\r\n"; got != want {
		t.Fatalf("LCS LEN reply=%q want=%q", got, want)
	}
}

func TestLCSIndexes(t *testing.T) {
	serv := newLCSTestServer(t)

	reply, err := serv.Execute([][]byte{[]byte("LCS"), []byte("key1"), []byte("key2"), []byte("IDX")})
	if err != nil {
		t.Fatalf("LCS IDX failed: %v", err)
	}

	want := "*4\r\n" +
		"$7\r\nmatches\r\n" +
		"*2\r\n" +
		"*2\r\n*2\r\n:4\r\n:7\r\n*2\r\n:5\r\n:8\r\n" +
		"*2\r\n*2\r\n:2\r\n:3\r\n*2\r\n:0\r\n:1\r\n" +
		"$3\r\nlen\r\n:6\r\n"
	if got := string(reply); got != want {
		t.Fatalf("LCS IDX reply=%q want=%q", got, want)
	}
}

func TestLCSIndexesMinMatchAndLength(t *testing.T) {
	serv := newLCSTestServer(t)

	reply, err := serv.Execute([][]byte{
		[]byte("LCS"), []byte("key1"), []byte("key2"),
		[]byte("IDX"), []byte("MINMATCHLEN"), []byte("4"), []byte("WITHMATCHLEN"),
	})
	if err != nil {
		t.Fatalf("LCS IDX MINMATCHLEN WITHMATCHLEN failed: %v", err)
	}

	want := "*4\r\n" +
		"$7\r\nmatches\r\n" +
		"*1\r\n" +
		"*3\r\n*2\r\n:4\r\n:7\r\n*2\r\n:5\r\n:8\r\n:4\r\n" +
		"$3\r\nlen\r\n:6\r\n"
	if got := string(reply); got != want {
		t.Fatalf("LCS filtered reply=%q want=%q", got, want)
	}
}

func TestLCSMissingKeysAreEmptyStrings(t *testing.T) {
	serv := New(engine.New())

	reply, err := serv.Execute([][]byte{[]byte("LCS"), []byte("missing-a"), []byte("missing-b")})
	if err != nil {
		t.Fatalf("LCS missing keys failed: %v", err)
	}
	if got, want := string(reply), "$0\r\n\r\n"; got != want {
		t.Fatalf("LCS missing reply=%q want=%q", got, want)
	}

	reply, err = serv.Execute([][]byte{[]byte("LCS"), []byte("missing-a"), []byte("missing-b"), []byte("LEN")})
	if err != nil {
		t.Fatalf("LCS LEN missing keys failed: %v", err)
	}
	if got, want := string(reply), ":0\r\n"; got != want {
		t.Fatalf("LCS LEN missing reply=%q want=%q", got, want)
	}
}

func TestLCSWrongTypeAndOptionErrors(t *testing.T) {
	serv := New(engine.New())
	if _, err := serv.Execute([][]byte{[]byte("LPUSH"), []byte("list"), []byte("x")}); err != nil {
		t.Fatalf("LPUSH failed: %v", err)
	}
	if _, err := serv.Execute([][]byte{[]byte("SET"), []byte("str"), []byte("x")}); err != nil {
		t.Fatalf("SET failed: %v", err)
	}

	_, err := serv.Execute([][]byte{[]byte("LCS"), []byte("list"), []byte("str")})
	if err == nil || err.Error() != "ERR The specified keys must contain string values" {
		t.Fatalf("wrong-type error=%v", err)
	}

	_, err = serv.Execute([][]byte{[]byte("LCS"), []byte("str"), []byte("str"), []byte("LEN"), []byte("IDX")})
	if err == nil || err.Error() != "ERR If you want both the length and indexes, please just use IDX." {
		t.Fatalf("LEN+IDX error=%v", err)
	}

	_, err = serv.Execute([][]byte{[]byte("LCS"), []byte("str"), []byte("str"), []byte("MINMATCHLEN"), []byte("nope")})
	if err == nil || err.Error() != "ERR value is not an integer or out of range" {
		t.Fatalf("MINMATCHLEN integer error=%v", err)
	}

	_, err = serv.Execute([][]byte{[]byte("LCS"), []byte("str"), []byte("str"), []byte("MINMATCHLEN")})
	if err == nil || err.Error() != "ERR syntax error" {
		t.Fatalf("MINMATCHLEN arity error=%v", err)
	}

	_, err = serv.Execute([][]byte{[]byte("LCS"), []byte("str"), []byte("str"), []byte("BOGUS")})
	if err == nil || !strings.Contains(err.Error(), "syntax error") {
		t.Fatalf("unknown option error=%v", err)
	}
}

func TestLCSNegativeMinMatchLenClampsToZero(t *testing.T) {
	serv := newLCSTestServer(t)

	reply, err := serv.Execute([][]byte{
		[]byte("LCS"), []byte("key1"), []byte("key2"),
		[]byte("IDX"), []byte("MINMATCHLEN"), []byte("-1"),
	})
	if err != nil {
		t.Fatalf("LCS negative MINMATCHLEN failed: %v", err)
	}
	if !strings.Contains(string(reply), ":2\r\n") {
		t.Fatalf("expected both match ranges, got %q", reply)
	}
}
