package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestVectorSetAttributes(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "VADD", "vec", "VALUES", "2", "1", "2", "a")

	if got := execute(t, s, "VGETATTR", "vec", "a"); got != "$-1\r\n" {
		t.Fatalf("initial VGETATTR=%q", got)
	}
	if got := execute(t, s, "VSETATTR", "vec", "a", "{\"year\":1960,\"kind\":\"fruit\"}"); got != ":1\r\n" {
		t.Fatalf("VSETATTR=%q", got)
	}
	got := execute(t, s, "VGETATTR", "vec", "a")
	if !strings.Contains(got, "year") || !strings.Contains(got, "1960") || !strings.Contains(got, "fruit") {
		t.Fatalf("VGETATTR=%q", got)
	}

	if got := execute(t, s, "VSETATTR", "vec", "a", ""); got != ":1\r\n" {
		t.Fatalf("VSETATTR delete=%q", got)
	}
	if got := execute(t, s, "VGETATTR", "vec", "a"); got != "$-1\r\n" {
		t.Fatalf("VGETATTR after delete=%q", got)
	}

	if got := execute(t, s, "VSETATTR", "vec", "missing", "{\"x\":1}"); got != ":0\r\n" {
		t.Fatalf("VSETATTR missing member=%q", got)
	}
	if got := execute(t, s, "VSETATTR", "missing", "a", "{\"x\":1}"); got != ":0\r\n" {
		t.Fatalf("VSETATTR missing key=%q", got)
	}
}

func TestVectorSetAttributesRejectInvalidJSON(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "VADD", "vec", "VALUES", "2", "1", "2", "a")

	for _, value := range []string{"not-json", "[]", "123"} {
		raw := [][]byte{
			[]byte("VSETATTR"), []byte("vec"), []byte("a"), []byte(value),
		}
		_, err := s.Execute(raw)
		if err == nil {
			t.Fatalf("VSETATTR accepted %q", value)
		}
	}
}

func TestVectorSetRandomMember(t *testing.T) {
	s := New(engine.New())
	for _, name := range []string{"a", "b", "c"} {
		execute(t, s, "VADD", "vec", "VALUES", "2", "1", "2", name)
	}

	got := execute(t, s, "VRANDMEMBER", "vec")
	if got == "$-1\r\n" {
		t.Fatal("VRANDMEMBER returned null for populated set")
	}

	got = execute(t, s, "VRANDMEMBER", "vec", "2")
	if !strings.HasPrefix(got, "*2\r\n") {
		t.Fatalf("VRANDMEMBER 2=%q", got)
	}

	got = execute(t, s, "VRANDMEMBER", "vec", "10")
	if !strings.HasPrefix(got, "*3\r\n") {
		t.Fatalf("VRANDMEMBER oversized count=%q", got)
	}

	got = execute(t, s, "VRANDMEMBER", "vec", "-5")
	if !strings.HasPrefix(got, "*5\r\n") {
		t.Fatalf("VRANDMEMBER negative=%q", got)
	}

	if got := execute(t, s, "VRANDMEMBER", "missing"); got != "$-1\r\n" {
		t.Fatalf("VRANDMEMBER missing=%q", got)
	}
	if got := execute(t, s, "VRANDMEMBER", "missing", "2"); got != "*0\r\n" {
		t.Fatalf("VRANDMEMBER missing count=%q", got)
	}
}

func TestVectorSetInfo(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "VADD", "vec", "VALUES", "3", "1", "2", "3", "a")
	execute(t, s, "VADD", "vec", "VALUES", "3", "4", "5", "6", "b")

	got := execute(t, s, "VINFO", "vec")
	for _, want := range []string{
		"quant-type", "fp32",
		"vector-dim", ":3\r\n",
		"size", ":2\r\n",
		"max-level", "vset-uid", "hnsw-max-node-uid",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("VINFO missing %q: %q", want, got)
		}
	}

	if got := execute(t, s, "VINFO", "missing"); got != "*-1\r\n" {
		t.Fatalf("VINFO missing=%q", got)
	}
}
