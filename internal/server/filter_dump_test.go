package server

import (
	"bytes"
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestBloomScanDumpLoadChunkRoundTrip(t *testing.T) {
	store := engine.New()
	s := New(store)

	execute(t, s, "BF.RESERVE", "bf", "0.01", "100")
	execute(t, s, "BF.MADD", "bf", "alpha", "beta", "gamma")

	dump, err := store.BloomDump("bf")
	if err != nil {
		t.Fatal(err)
	}

	reply, err := s.Execute([][]byte{
		[]byte("BF.SCANDUMP"), []byte("bf"), []byte("0"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(reply, []byte("*2\r\n:1\r\n$")) {
		t.Fatalf("BF.SCANDUMP start=%q", reply)
	}

	if got := execute(t, s, "BF.SCANDUMP", "bf", "1"); got != "*2\r\n:0\r\n$-1\r\n" {
		t.Fatalf("BF.SCANDUMP terminal=%q", got)
	}

	res, err := s.Execute([][]byte{
		[]byte("BF.LOADCHUNK"), []byte("bf-copy"), []byte("1"), dump,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(res) != "+OK\r\n" {
		t.Fatalf("BF.LOADCHUNK=%q", res)
	}

	for _, item := range []string{"alpha", "beta", "gamma"} {
		if got := execute(t, s, "BF.EXISTS", "bf-copy", item); got != ":1\r\n" {
			t.Fatalf("BF.EXISTS %s=%q", item, got)
		}
	}
	if got := execute(t, s, "BF.CARD", "bf-copy"); got != ":3\r\n" {
		t.Fatalf("BF.CARD copy=%q", got)
	}
}

func TestCuckooScanDumpLoadChunkRoundTrip(t *testing.T) {
	store := engine.New()
	s := New(store)

	execute(t, s, "CF.RESERVE", "cf", "100")
	execute(t, s, "CF.ADD", "cf", "alpha")
	execute(t, s, "CF.ADD", "cf", "beta")
	execute(t, s, "CF.ADD", "cf", "gamma")

	dump, err := store.CuckooDump("cf")
	if err != nil {
		t.Fatal(err)
	}

	reply, err := s.Execute([][]byte{
		[]byte("CF.SCANDUMP"), []byte("cf"), []byte("0"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(reply, []byte("*2\r\n:1\r\n$")) {
		t.Fatalf("CF.SCANDUMP start=%q", reply)
	}

	if got := execute(t, s, "CF.SCANDUMP", "cf", "1"); got != "*2\r\n:0\r\n$-1\r\n" {
		t.Fatalf("CF.SCANDUMP terminal=%q", got)
	}

	res, err := s.Execute([][]byte{
		[]byte("CF.LOADCHUNK"), []byte("cf-copy"), []byte("1"), dump,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(res) != "+OK\r\n" {
		t.Fatalf("CF.LOADCHUNK=%q", res)
	}

	for _, item := range []string{"alpha", "beta", "gamma"} {
		if got := execute(t, s, "CF.EXISTS", "cf-copy", item); got != ":1\r\n" {
			t.Fatalf("CF.EXISTS %s=%q", item, got)
		}
	}
}

func TestFilterDumpLoadErrors(t *testing.T) {
	store := engine.New()
	s := New(store)

	execute(t, s, "BF.ADD", "bf", "x")
	execute(t, s, "CF.ADD", "cf", "x")
	execute(t, s, "SET", "plain", "value")

	assertErrContains := func(want string, args ...[]byte) {
		t.Helper()
		_, err := s.Execute(args)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q error=%v want substring %q", args, err, want)
		}
	}

	assertErrContains("Second argument must be numeric",
		[]byte("BF.SCANDUMP"), []byte("bf"), []byte("bad"))
	assertErrContains("Invalid position",
		[]byte("CF.SCANDUMP"), []byte("cf"), []byte("-1"))
	assertErrContains("Invalid position",
		[]byte("BF.LOADCHUNK"), []byte("copy"), []byte("2"), []byte("x"))
	assertErrContains("Invalid position",
		[]byte("CF.LOADCHUNK"), []byte("copy"), []byte("0"), []byte("x"))

	bfDump, _ := store.BloomDump("bf")
	assertErrContains("item exists",
		[]byte("BF.LOADCHUNK"), []byte("bf"), []byte("1"), bfDump)

	assertErrContains("WRONGTYPE",
		[]byte("BF.SCANDUMP"), []byte("plain"), []byte("0"))
	assertErrContains("WRONGTYPE",
		[]byte("CF.SCANDUMP"), []byte("plain"), []byte("0"))
}
