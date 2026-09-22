package server

import (
	"testing"

	"snugkv/internal/engine"
)

func TestRedis810RejectsDynamicMissingJSONSetPath(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "JSON.SET", "doc", "$", `{"items":[{"score":1},{"score":2},{"score":3}]}`)

	if got := execute(t, s, "JSON.SET", "doc", "$.items[?(@.score >= 2)].expensive", "true"); got != "-ERR wrong static path\r\n" {
		t.Fatalf("dynamic missing path=%q", got)
	}

	if got := execute(t, s, "JSON.GET", "doc", "$.items[*].expensive"); got != "$2\r\n[]\r\n" {
		t.Fatalf("dynamic missing result=%q", got)
	}

	if got := execute(t, s, "JSON.SET", "doc", "$.items[1].expensive", "true"); got != "+OK\r\n" {
		t.Fatalf("static create=%q", got)
	}

	if got := execute(t, s, "JSON.GET", "doc", "$.items[1].expensive"); got != "$6\r\n[true]\r\n" {
		t.Fatalf("static create result=%q", got)
	}
}
