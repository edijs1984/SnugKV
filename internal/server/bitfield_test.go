package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestBitFieldBasicSignedUnsignedAndHashOffsets(t *testing.T) {
	s := New(engine.New())

	got := execute(t, s, "BITFIELD", "bf",
		"SET", "u8", "#0", "255",
		"SET", "i8", "#1", "-1",
		"GET", "u8", "0",
		"GET", "i8", "8",
	)
	want := "*4\r\n:0\r\n:0\r\n:255\r\n:-1\r\n"
	if got != want {
		t.Fatalf("BITFIELD basic=%q want=%q", got, want)
	}

	if got := execute(t, s, "GET", "bf"); got != "$2\r\n\xff\xff\r\n" {
		t.Fatalf("stored bytes=%q", got)
	}
}

func TestBitFieldUnalignedAndChainedOperations(t *testing.T) {
	s := New(engine.New())

	got := execute(t, s, "BITFIELD", "bf",
		"SET", "u5", "7", "23",
		"GET", "u5", "7",
		"INCRBY", "u5", "7", "1",
		"GET", "u5", "7",
	)
	want := "*4\r\n:0\r\n:23\r\n:24\r\n:24\r\n"
	if got != want {
		t.Fatalf("BITFIELD unaligned=%q want=%q", got, want)
	}
}

func TestBitFieldOverflowModes(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "BITFIELD", "wrap",
		"SET", "i8", "0", "127",
		"INCRBY", "i8", "0", "1",
	); got != "*2\r\n:0\r\n:-128\r\n" {
		t.Fatalf("WRAP signed=%q", got)
	}

	if got := execute(t, s, "BITFIELD", "sat",
		"SET", "u2", "0", "3",
		"OVERFLOW", "SAT",
		"INCRBY", "u2", "0", "1",
	); got != "*2\r\n:0\r\n:3\r\n" {
		t.Fatalf("SAT unsigned=%q", got)
	}

	if got := execute(t, s, "BITFIELD", "fail",
		"SET", "u2", "0", "3",
		"OVERFLOW", "FAIL",
		"INCRBY", "u2", "0", "1",
		"GET", "u2", "0",
	); got != "*3\r\n:0\r\n$-1\r\n:3\r\n" {
		t.Fatalf("FAIL unsigned=%q", got)
	}
}

func TestBitFieldSetOverflowModes(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "BITFIELD", "wrap", "SET", "u8", "0", "-1", "GET", "u8", "0"); got != "*2\r\n:0\r\n:255\r\n" {
		t.Fatalf("SET WRAP unsigned negative=%q", got)
	}

	if got := execute(t, s, "BITFIELD", "sat", "OVERFLOW", "SAT", "SET", "i4", "0", "99", "GET", "i4", "0"); got != "*2\r\n:0\r\n:7\r\n" {
		t.Fatalf("SET SAT signed=%q", got)
	}

	if got := execute(t, s, "BITFIELD", "fail", "OVERFLOW", "FAIL", "SET", "i4", "0", "99", "GET", "i4", "0"); got != "*2\r\n$-1\r\n:0\r\n" {
		t.Fatalf("SET FAIL signed=%q", got)
	}
}

func TestBitFieldROMissingAndValidation(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "BITFIELD_RO", "missing", "GET", "u8", "0", "GET", "i5", "#2"); got != "*2\r\n:0\r\n:0\r\n" {
		t.Fatalf("BITFIELD_RO missing=%q", got)
	}

	assertErrContains := func(want string, args ...string) {
		t.Helper()
		raw := make([][]byte, len(args))
		for i := range args {
			raw[i] = []byte(args[i])
		}
		_, err := s.Execute(raw)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q error=%v want substring %q", args, err, want)
		}
	}

	assertErrContains("only supports the GET", "BITFIELD_RO", "bf", "SET", "u8", "0", "1")
	assertErrContains("Invalid bitfield type", "BITFIELD", "bf", "GET", "u64", "0")
	assertErrContains("bit offset is not an integer or out of range", "BITFIELD", "bf", "GET", "u8", "-1")
	assertErrContains("Invalid OVERFLOW type", "BITFIELD", "bf", "OVERFLOW", "NOPE", "GET", "u8", "0")
}

func TestBitFieldWrongType(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "HSET", "hash", "f", "v")

	for _, command := range [][][]byte{
		{[]byte("BITFIELD"), []byte("hash"), []byte("GET"), []byte("u8"), []byte("0")},
		{[]byte("BITFIELD_RO"), []byte("hash"), []byte("GET"), []byte("u8"), []byte("0")},
	} {
		if _, err := s.Execute(command); err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE") {
			t.Fatalf("expected WRONGTYPE for %q, err=%v", command, err)
		}
	}
}
