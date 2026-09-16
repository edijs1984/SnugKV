package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestCopyPreservesStreamGroupsAndPendingState(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "XADD", "copy:stream", "1-0", "v", "one")
	execute(t, s, "XADD", "copy:stream", "2-0", "v", "two")
	execute(t, s, "XGROUP", "CREATE", "copy:stream", "workers", "0-0")

	got := execute(t, s, "XREADGROUP", "GROUP", "workers", "c1", "COUNT", "1", "STREAMS", "copy:stream", ">")
	if !strings.Contains(got, "$3\r\n1-0\r\n") {
		t.Fatalf("source XREADGROUP = %q", got)
	}

	if got, err := executeCopyTest(t, s, "COPY", "copy:stream", "copy:stream:clone"); err != nil || got != ":1\r\n" {
		t.Fatalf("COPY stream = %q, %v", got, err)
	}

	pending := execute(t, s, "XPENDING", "copy:stream:clone", "workers")
	if !strings.HasPrefix(pending, "*4\r\n:1\r\n") ||
		!strings.Contains(pending, "$3\r\n1-0\r\n") ||
		!strings.Contains(pending, "$2\r\nc1\r\n") {
		t.Fatalf("copied stream pending state = %q", pending)
	}

	// Group state is independent after COPY: acknowledging on the clone must not
	// remove the source stream's pending entry.
	if got := execute(t, s, "XACK", "copy:stream:clone", "workers", "1-0"); got != ":1\r\n" {
		t.Fatalf("clone XACK = %q", got)
	}
	if got := execute(t, s, "XPENDING", "copy:stream:clone", "workers"); !strings.HasPrefix(got, "*4\r\n:0\r\n") {
		t.Fatalf("clone pending after XACK = %q", got)
	}
	if got := execute(t, s, "XPENDING", "copy:stream", "workers"); !strings.HasPrefix(got, "*4\r\n:1\r\n") {
		t.Fatalf("source pending changed after clone XACK = %q", got)
	}
}
