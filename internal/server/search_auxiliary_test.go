package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestSearchDictionaries(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "FT.DICTADD", "cities", "riga", "tallinn", "riga"); got != ":2\r\n" {
		t.Fatalf("FT.DICTADD=%q", got)
	}
	got := execute(t, s, "FT.DICTDUMP", "cities")
	if !strings.Contains(got, "riga") || !strings.Contains(got, "tallinn") {
		t.Fatalf("FT.DICTDUMP=%q", got)
	}
	if got := execute(t, s, "FT.DICTDEL", "cities", "riga", "missing"); got != ":1\r\n" {
		t.Fatalf("FT.DICTDEL=%q", got)
	}
	got = execute(t, s, "FT.DICTDUMP", "cities")
	if strings.Contains(got, "riga") || !strings.Contains(got, "tallinn") {
		t.Fatalf("FT.DICTDUMP after delete=%q", got)
	}
}

func TestSearchSpellCheck(t *testing.T) {
	s := New(engine.New())
	execute(t, s,
		"FT.CREATE", "idx", "ON", "JSON",
		"SCHEMA", "$.title", "AS", "title", "TEXT",
	)
	execute(t, s, "JSON.SET", "doc:1", "$", "{\"title\":\"hello world\"}")
	execute(t, s, "JSON.SET", "doc:2", "$", "{\"title\":\"hello help\"}")
	execute(t, s, "JSON.SET", "doc:3", "$", "{\"title\":\"help wanted\"}")

	got := execute(t, s, "FT.SPELLCHECK", "idx", "held", "DISTANCE", "2")
	for _, want := range []string{"TERM", "held", "hello", "help"} {
		if !strings.Contains(got, want) {
			t.Fatalf("FT.SPELLCHECK missing %q: %q", want, got)
		}
	}

	execute(t, s, "FT.DICTADD", "extra", "helm")
	got = execute(t, s, "FT.SPELLCHECK", "idx", "held", "DISTANCE", "2", "TERMS", "INCLUDE", "extra")
	if !strings.Contains(got, "helm") {
		t.Fatalf("FT.SPELLCHECK custom dictionary=%q", got)
	}

	got = execute(t, s, "FT.SPELLCHECK", "idx", "hello")
	if got != "*0\r\n" {
		t.Fatalf("correct term produced suggestions: %q", got)
	}
}

func TestSearchSynonyms(t *testing.T) {
	s := New(engine.New())
	execute(t, s,
		"FT.CREATE", "idx", "ON", "JSON",
		"SCHEMA", "$.title", "AS", "title", "TEXT",
	)

	if got := execute(t, s, "FT.SYNUPDATE", "idx", "g1", "hello", "hi", "shalom"); got != "+OK\r\n" {
		t.Fatalf("FT.SYNUPDATE=%q", got)
	}
	if got := execute(t, s, "FT.SYNUPDATE", "idx", "g2", "SKIPINITIALSCAN", "hello", "hey"); got != "+OK\r\n" {
		t.Fatalf("FT.SYNUPDATE skip=%q", got)
	}

	got := execute(t, s, "FT.SYNDUMP", "idx")
	for _, want := range []string{"hello", "hi", "shalom", "hey", "g1", "g2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("FT.SYNDUMP missing %q: %q", want, got)
		}
	}
}

func TestSearchSuggestions(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "FT.SUGADD", "auto", "hello world", "100"); got != ":1\r\n" {
		t.Fatalf("SUGADD 1=%q", got)
	}
	execute(t, s, "FT.SUGADD", "auto", "hello there", "90")
	execute(t, s, "FT.SUGADD", "auto", "help me", "80", "PAYLOAD", "payload-help")
	execute(t, s, "FT.SUGADD", "auto", "hero", "70")

	if got := execute(t, s, "FT.SUGLEN", "auto"); got != ":4\r\n" {
		t.Fatalf("SUGLEN=%q", got)
	}

	got := execute(t, s, "FT.SUGGET", "auto", "he", "MAX", "2")
	if !strings.Contains(got, "hello world") || !strings.Contains(got, "hello there") || strings.Contains(got, "help me") {
		t.Fatalf("SUGGET ranking=%q", got)
	}

	got = execute(t, s, "FT.SUGGET", "auto", "help", "WITHSCORES", "WITHPAYLOADS")
	if !strings.Contains(got, "help me") || !strings.Contains(got, "80") || !strings.Contains(got, "payload-help") {
		t.Fatalf("SUGGET options=%q", got)
	}

	execute(t, s, "FT.SUGADD", "auto", "hero", "5", "INCR")
	got = execute(t, s, "FT.SUGGET", "auto", "hero", "WITHSCORES")
	if !strings.Contains(got, "75") {
		t.Fatalf("SUGADD INCR=%q", got)
	}

	if got := execute(t, s, "FT.SUGDEL", "auto", "hero"); got != ":1\r\n" {
		t.Fatalf("SUGDEL=%q", got)
	}
	if got := execute(t, s, "FT.SUGLEN", "auto"); got != ":3\r\n" {
		t.Fatalf("SUGLEN after delete=%q", got)
	}
}

func TestSearchAuxiliaryErrors(t *testing.T) {
	s := New(engine.New())

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

	assertErrContains("Index not found", "FT.SPELLCHECK", "missing", "hello")
	assertErrContains("Index not found", "FT.SYNDUMP", "missing")
	assertErrContains("invalid score", "FT.SUGADD", "auto", "x", "NaN")
}
