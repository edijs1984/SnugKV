package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestStreamV3MinIDAndLifetimeMetadata(t *testing.T) {
	s := New(engine.New())
	for _, id := range []string{"1-0", "2-0", "3-0", "4-0"} {
		if _, err := s.Execute([][]byte{[]byte("XADD"), []byte("events"), []byte(id), []byte("v"), []byte(id)}); err != nil {
			t.Fatal(err)
		}
	}
	response, err := s.Execute([][]byte{[]byte("XDEL"), []byte("events"), []byte("2-0")})
	if err != nil || string(response) != ":1\r\n" {
		t.Fatalf("XDEL = %q, %v", response, err)
	}
	response, err = s.Execute([][]byte{[]byte("XTRIM"), []byte("events"), []byte("MINID"), []byte("4-0")})
	if err != nil || string(response) != ":2\r\n" {
		t.Fatalf("XTRIM MINID = %q, %v", response, err)
	}
	response, err = s.Execute([][]byte{[]byte("XADD"), []byte("events"), []byte("MINID"), []byte("5-0"), []byte("5-0"), []byte("v"), []byte("five")})
	if err != nil || string(response) != "$3\r\n5-0\r\n" {
		t.Fatalf("XADD MINID = %q, %v", response, err)
	}
	info, err := s.Execute([][]byte{[]byte("XINFO"), []byte("STREAM"), []byte("events")})
	if err != nil {
		t.Fatal(err)
	}
	text := string(info)
	for _, want := range []string{"entries-added", ":5\r\n", "max-deleted-entry-id", "$3\r\n2-0\r\n"} {
		if !strings.Contains(text, want) {
			t.Fatalf("XINFO STREAM missing %q: %q", want, text)
		}
	}
}

func TestStreamV3MinIDLimit(t *testing.T) {
	s := New(engine.New())
	for _, id := range []string{"1-0", "2-0", "3-0"} {
		if _, err := s.Execute([][]byte{[]byte("XADD"), []byte("events"), []byte(id), []byte("v"), []byte(id)}); err != nil {
			t.Fatal(err)
		}
	}
	response, err := s.Execute([][]byte{[]byte("XTRIM"), []byte("events"), []byte("MINID"), []byte("~"), []byte("4-0"), []byte("LIMIT"), []byte("1")})
	if err != nil || string(response) != ":1\r\n" {
		t.Fatalf("limited XTRIM MINID = %q, %v", response, err)
	}
	response, err = s.Execute([][]byte{[]byte("XLEN"), []byte("events")})
	if err != nil || string(response) != ":2\r\n" {
		t.Fatalf("XLEN = %q, %v", response, err)
	}
}
