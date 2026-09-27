package server

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestVectorSetCoreValues(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "VADD", "vec", "VALUES", "3", "1", "2", "3", "alice"); got != ":1\r\n" {
		t.Fatalf("VADD insert=%q", got)
	}
	if got := execute(t, s, "VCARD", "vec"); got != ":1\r\n" {
		t.Fatalf("VCARD=%q", got)
	}
	if got := execute(t, s, "VDIM", "vec"); got != ":3\r\n" {
		t.Fatalf("VDIM=%q", got)
	}
	if got := execute(t, s, "VISMEMBER", "vec", "alice"); got != ":1\r\n" {
		t.Fatalf("VISMEMBER alice=%q", got)
	}
	if got := execute(t, s, "VISMEMBER", "vec", "missing"); got != ":0\r\n" {
		t.Fatalf("VISMEMBER missing=%q", got)
	}

	got := execute(t, s, "VEMB", "vec", "alice")
	for _, want := range []string{"$1\r\n1\r\n", "$1\r\n2\r\n", "$1\r\n3\r\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("VEMB missing %q: %q", want, got)
		}
	}

	if got := execute(t, s, "VADD", "vec", "VALUES", "3", "4", "5", "6", "alice"); got != ":0\r\n" {
		t.Fatalf("VADD update=%q", got)
	}
	if got := execute(t, s, "VCARD", "vec"); got != ":1\r\n" {
		t.Fatalf("VCARD after update=%q", got)
	}
}

func TestVectorSetFP32AndRaw(t *testing.T) {
	s := New(engine.New())

	blob := make([]byte, 8)
	binary.LittleEndian.PutUint32(blob[0:4], math.Float32bits(1.25))
	binary.LittleEndian.PutUint32(blob[4:8], math.Float32bits(-2.5))

	raw := [][]byte{
		[]byte("VADD"), []byte("fp"), []byte("FP32"), blob, []byte("p"),
	}
	reply, err := s.Execute(raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != ":1\r\n" {
		t.Fatalf("VADD FP32=%q", reply)
	}

	if got := execute(t, s, "VEMB", "fp", "p"); !strings.Contains(got, "1.25") || !strings.Contains(got, "-2.5") {
		t.Fatalf("VEMB FP32=%q", got)
	}

	got := execute(t, s, "VEMB", "fp", "p", "RAW")
	if !strings.Contains(got, "+fp32\r\n") {
		t.Fatalf("VEMB RAW missing fp32 type: %q", got)
	}
	if !strings.Contains(got, "$8\r\n") {
		t.Fatalf("VEMB RAW wrong blob length: %q", got)
	}
}

func TestVectorSetTTLAndRemove(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "VADD", "vec", "VALUES", "2", "1", "1", "a")
	execute(t, s, "PEXPIRE", "vec", "60000")
	execute(t, s, "VADD", "vec", "VALUES", "2", "2", "2", "b")

	if got := execute(t, s, "PTTL", "vec"); got == ":-1\r\n" || got == ":-2\r\n" {
		t.Fatalf("VADD lost TTL: %q", got)
	}

	if got := execute(t, s, "VREM", "vec", "a"); got != ":1\r\n" {
		t.Fatalf("VREM a=%q", got)
	}
	if got := execute(t, s, "VREM", "vec", "a"); got != ":0\r\n" {
		t.Fatalf("VREM missing member=%q", got)
	}
	if got := execute(t, s, "VREM", "vec", "b"); got != ":1\r\n" {
		t.Fatalf("VREM last=%q", got)
	}
	if got := execute(t, s, "EXISTS", "vec"); got != ":0\r\n" {
		t.Fatalf("last VREM did not remove key: %q", got)
	}
	if got := execute(t, s, "VCARD", "vec"); got != ":0\r\n" {
		t.Fatalf("VCARD missing=%q", got)
	}
}

func TestVectorSetCoreErrors(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "plain", "x")
	execute(t, s, "VADD", "vec", "VALUES", "2", "1", "2", "a")

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

	assertErrContains("dimension mismatch", "VADD", "vec", "VALUES", "3", "1", "2", "3", "b")
	assertErrContains("WRONGTYPE", "VCARD", "plain")
	assertErrContains("WRONGTYPE", "VEMB", "plain", "x")
	assertErrContains("vector set not found", "VDIM", "missing")
	assertErrContains("invalid vector value", "VADD", "bad", "VALUES", "2", "NaN", "1", "x")
}
