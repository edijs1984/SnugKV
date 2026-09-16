package server

import (
	"errors"
	"testing"
)

func TestNoScriptErrorKeepsRedisWirePrefix(t *testing.T) {
	got := string(errorResponse(errors.New("NOSCRIPT No matching script. Please use EVAL.")))
	want := "-NOSCRIPT No matching script. Please use EVAL.\r\n"
	if got != want {
		t.Fatalf("NOSCRIPT response = %q, want %q", got, want)
	}
}
