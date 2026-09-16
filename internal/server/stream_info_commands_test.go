package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestXInfoStreamGroupsConsumersFullAndHelp(t *testing.T) {
	s := New(engine.New())
	for _, id := range []string{"1-0", "2-0", "3-0"} {
		if _, err := s.Execute([][]byte{[]byte("XADD"), []byte("events"), []byte(id), []byte("v"), []byte(id)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Execute([][]byte{[]byte("XGROUP"), []byte("CREATE"), []byte("events"), []byte("workers"), []byte("0-0"), []byte("ENTRIESREAD"), []byte("0")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute([][]byte{[]byte("XREADGROUP"), []byte("GROUP"), []byte("workers"), []byte("worker-1"), []byte("COUNT"), []byte("1"), []byte("STREAMS"), []byte("events"), []byte(">")}); err != nil {
		t.Fatal(err)
	}

	response, err := s.Execute([][]byte{[]byte("XINFO"), []byte("STREAM"), []byte("events")})
	if err != nil {
		t.Fatal(err)
	}
	text := string(response)
	for _, want := range []string{"length", "last-generated-id", "max-deleted-entry-id", "entries-added", "recorded-first-entry-id", "first-entry", "last-entry"} {
		if !strings.Contains(text, want) {
			t.Fatalf("XINFO STREAM missing %q: %q", want, text)
		}
	}

	response, err = s.Execute([][]byte{[]byte("XINFO"), []byte("STREAM"), []byte("events"), []byte("FULL"), []byte("COUNT"), []byte("1")})
	if err != nil {
		t.Fatal(err)
	}
	text = string(response)
	for _, want := range []string{"entries", "groups", "pel-count", "seen-time", "active-time"} {
		if !strings.Contains(text, want) {
			t.Fatalf("XINFO STREAM FULL missing %q: %q", want, text)
		}
	}

	response, err = s.Execute([][]byte{[]byte("XINFO"), []byte("GROUPS"), []byte("events")})
	if err != nil {
		t.Fatal(err)
	}
	text = string(response)
	for _, want := range []string{"workers", "consumers", "pending", "last-delivered-id", "entries-read", "lag"} {
		if !strings.Contains(text, want) {
			t.Fatalf("XINFO GROUPS missing %q: %q", want, text)
		}
	}

	response, err = s.Execute([][]byte{[]byte("XINFO"), []byte("CONSUMERS"), []byte("events"), []byte("workers")})
	if err != nil {
		t.Fatal(err)
	}
	text = string(response)
	for _, want := range []string{"worker-1", "pending", "idle", "inactive"} {
		if !strings.Contains(text, want) {
			t.Fatalf("XINFO CONSUMERS missing %q: %q", want, text)
		}
	}

	response, err = s.Execute([][]byte{[]byte("XINFO"), []byte("HELP")})
	if err != nil || !strings.Contains(string(response), "CONSUMERS") || !strings.Contains(string(response), "STREAM") {
		t.Fatalf("XINFO HELP = %q, %v", response, err)
	}
}

func TestXInfoErrorsAndSyntax(t *testing.T) {
	s := New(engine.New())
	if _, err := s.Execute([][]byte{[]byte("XINFO"), []byte("STREAM"), []byte("missing")}); err == nil || err.Error() != "ERR no such key" {
		t.Fatalf("missing stream error = %v", err)
	}
	if _, err := s.Execute([][]byte{[]byte("SET"), []byte("plain"), []byte("value")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute([][]byte{[]byte("XINFO"), []byte("STREAM"), []byte("plain")}); err == nil || !strings.HasPrefix(err.Error(), "WRONGTYPE ") {
		t.Fatalf("wrongtype error = %v", err)
	}
	if _, err := s.Execute([][]byte{[]byte("XINFO"), []byte("STREAM"), []byte("plain"), []byte("COUNT"), []byte("1")}); err == nil {
		t.Fatal("expected STREAM COUNT without FULL to fail")
	}
	if _, err := s.Execute([][]byte{[]byte("XINFO"), []byte("STREAM"), []byte("plain"), []byte("FULL"), []byte("COUNT"), []byte("-1")}); err == nil {
		t.Fatal("expected negative COUNT to fail")
	}
	if _, err := s.Execute([][]byte{[]byte("XINFO"), []byte("BOGUS")}); err == nil {
		t.Fatal("expected unknown subcommand to fail")
	}
}
