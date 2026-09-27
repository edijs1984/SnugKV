package server

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestVectorSetSimilarityByElement(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "VADD", "vec", "VALUES", "2", "1", "0", "east")
	execute(t, s, "VADD", "vec", "VALUES", "2", "0", "1", "north")
	execute(t, s, "VADD", "vec", "VALUES", "2", "-1", "0", "west")

	got := execute(t, s, "VSIM", "vec", "ELE", "east", "WITHSCORES", "COUNT", "3")
	wantOrder := []string{"east", "north", "west"}
	last := -1
	for _, name := range wantOrder {
		idx := strings.Index(got, name)
		if idx < 0 || idx <= last {
			t.Fatalf("VSIM order wrong for %q: %q", name, got)
		}
		last = idx
	}
	for _, score := range []string{"$1\r\n1\r\n", "$3\r\n0.5\r\n", "$1\r\n0\r\n"} {
		if !strings.Contains(got, score) {
			t.Fatalf("VSIM missing score %q: %q", score, got)
		}
	}
}

func TestVectorSetSimilarityValuesFP32AndEpsilon(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "VADD", "vec", "VALUES", "2", "1", "0", "a")
	execute(t, s, "VADD", "vec", "VALUES", "2", "0.9", "0.1", "b")
	execute(t, s, "VADD", "vec", "VALUES", "2", "0", "1", "c")

	got := execute(t, s, "VSIM", "vec", "VALUES", "2", "1", "0", "COUNT", "2")
	if !strings.Contains(got, "a") || !strings.Contains(got, "b") || strings.Contains(got, "c") {
		t.Fatalf("VSIM VALUES=%q", got)
	}

	blob := make([]byte, 8)
	binary.LittleEndian.PutUint32(blob[:4], math.Float32bits(1))
	binary.LittleEndian.PutUint32(blob[4:], math.Float32bits(0))
	raw := [][]byte{[]byte("VSIM"), []byte("vec"), []byte("FP32"), blob, []byte("COUNT"), []byte("1")}
	reply, err := s.Execute(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reply), "a") {
		t.Fatalf("VSIM FP32=%q", reply)
	}

	got = execute(t, s, "VSIM", "vec", "ELE", "a", "EPSILON", "0.01", "WITHSCORES")
	if !strings.Contains(got, "a") || strings.Contains(got, "c") {
		t.Fatalf("VSIM EPSILON=%q", got)
	}
}

func TestVectorSetSimilarityFilterAndAttrs(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "VADD", "movies", "VALUES", "2", "1", "0", "a", "SETATTR", "{\"year\":1985,\"genre\":\"action\"}")
	execute(t, s, "VADD", "movies", "VALUES", "2", "0.9", "0.1", "b", "SETATTR", "{\"year\":1970,\"genre\":\"action\"}")
	execute(t, s, "VADD", "movies", "VALUES", "2", "0.8", "0.2", "c", "SETATTR", "{\"year\":1990,\"genre\":\"drama\"}")

	got := execute(t, s, "VSIM", "movies", "ELE", "a",
		"FILTER", ".year >= 1980 && .genre == \"action\"",
		"WITHSCORES", "WITHATTRIBS", "COUNT", "5")
	if !strings.Contains(got, "a") {
		t.Fatalf("filtered VSIM missing a: %q", got)
	}
	if strings.Contains(got, "$1\r\nb\r\n") || strings.Contains(got, "$1\r\nc\r\n") {
		t.Fatalf("filtered VSIM included excluded member: %q", got)
	}
	if !strings.Contains(got, "1985") || !strings.Contains(got, "action") {
		t.Fatalf("WITHATTRIBS missing attrs: %q", got)
	}
}

func TestVectorSetLinks(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "VADD", "vec", "VALUES", "2", "1", "0", "a")
	execute(t, s, "VADD", "vec", "VALUES", "2", "0.9", "0.1", "b")
	execute(t, s, "VADD", "vec", "VALUES", "2", "0", "1", "c")

	got := execute(t, s, "VLINKS", "vec", "a")
	if !strings.HasPrefix(got, "*1\r\n*2\r\n") {
		t.Fatalf("VLINKS shape=%q", got)
	}
	if strings.Index(got, "b") > strings.Index(got, "c") {
		t.Fatalf("VLINKS nearest order=%q", got)
	}

	got = execute(t, s, "VLINKS", "vec", "a", "WITHSCORES")
	if !strings.HasPrefix(got, "*1\r\n*4\r\n") || !strings.Contains(got, "0.5") {
		t.Fatalf("VLINKS WITHSCORES=%q", got)
	}

	if got := execute(t, s, "VLINKS", "missing", "a"); got != "$-1\r\n" {
		t.Fatalf("VLINKS missing key=%q", got)
	}
	if got := execute(t, s, "VLINKS", "vec", "missing"); got != "$-1\r\n" {
		t.Fatalf("VLINKS missing member=%q", got)
	}
}

func TestVectorSetSimilarityErrors(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "VADD", "vec", "VALUES", "2", "1", "0", "a")

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

	assertErrContains("element not found", "VSIM", "vec", "ELE", "missing")
	assertErrContains("dimension mismatch", "VSIM", "vec", "VALUES", "3", "1", "2", "3")
	assertErrContains("invalid epsilon", "VSIM", "vec", "ELE", "a", "EPSILON", "2")

	if got := execute(t, s, "VSIM", "missing", "VALUES", "2", "1", "0"); got != "*0\r\n" {
		t.Fatalf("VSIM missing key=%q", got)
	}
}
