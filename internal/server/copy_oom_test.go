package server

import (
	"errors"
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestCopyReplaceOOMLeavesSourceAndDestinationUnchanged(t *testing.T) {
	value := []byte(strings.Repeat("x", 24<<10))

	// Measure the deterministic accounted footprint of the starting state, then
	// allow only a small amount of headroom. Replacing the tiny destination with
	// another 24 KiB logical value must require additional arena capacity.
	probe, err := engine.NewWithOptions(engine.Options{Shards: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.Set("src", value, 0); err != nil {
		t.Fatal(err)
	}
	if err := probe.Set("dst", []byte("old"), 0); err != nil {
		t.Fatal(err)
	}
	limit := probe.Memory().AccountedBytes + 1024

	store, err := engine.NewWithOptions(engine.Options{Shards: 1, MaxMemory: limit})
	if err != nil {
		t.Fatal(err)
	}
	s := New(store)
	if got, err := executeCopyTest(t, s, "SET", "src", string(value)); err != nil || got != "+OK\r\n" {
		t.Fatalf("SET src = %q, %v", got, err)
	}
	if got, err := executeCopyTest(t, s, "SET", "dst", "old"); err != nil || got != "+OK\r\n" {
		t.Fatalf("SET dst = %q, %v", got, err)
	}

	if _, err := executeCopyTest(t, s, "COPY", "src", "dst", "REPLACE"); !errors.Is(err, engine.ErrOOM) {
		t.Fatalf("COPY error = %v, want OOM", err)
	}
	if got, _ := executeCopyTest(t, s, "GET", "dst"); got != "$3\r\nold\r\n" {
		t.Fatalf("destination changed after OOM: %q", got)
	}
	if got, _ := executeCopyTest(t, s, "STRLEN", "src"); got != ":24576\r\n" {
		t.Fatalf("source changed after OOM: %q", got)
	}
}
