package server

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func TestCommandList(t *testing.T) {
	s := New(engine.New())

	got := execute(t, s, "COMMAND", "LIST")
	if !strings.Contains(got, "get") || !strings.Contains(got, "set") || !strings.Contains(got, "ft.search") {
		t.Fatalf("COMMAND LIST missing commands: %q", got)
	}

	got = execute(t, s, "COMMAND", "LIST", "FILTERBY", "PATTERN", "ft.*")
	if !strings.Contains(got, "ft.search") || strings.Contains(got, "$3\r\nget\r\n") {
		t.Fatalf("COMMAND LIST pattern=%q", got)
	}

	got = execute(t, s, "COMMAND", "LIST", "FILTERBY", "ACLCAT", "read")
	if !strings.Contains(got, "get") {
		t.Fatalf("COMMAND LIST ACLCAT read=%q", got)
	}

	got = execute(t, s, "COMMAND", "LIST", "FILTERBY", "MODULE", "search")
	if got != "*0\r\n" {
		t.Fatalf("COMMAND LIST MODULE=%q", got)
	}
}

func TestTimeCommand(t *testing.T) {
	s := New(engine.New())
	before := time.Now().Unix()

	got := execute(t, s, "TIME")
	if !strings.HasPrefix(got, "*2\r\n") {
		t.Fatalf("TIME shape=%q", got)
	}

	parts := strings.Split(got, "\r\n")
	if len(parts) < 6 {
		t.Fatalf("TIME malformed=%q", got)
	}
	sec, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		t.Fatalf("TIME seconds parse=%v reply=%q", err, got)
	}
	usec, err := strconv.ParseInt(parts[4], 10, 64)
	if err != nil {
		t.Fatalf("TIME usec parse=%v reply=%q", err, got)
	}
	if sec < before || sec > time.Now().Unix()+1 {
		t.Fatalf("TIME seconds out of range: %d", sec)
	}
	if usec < 0 || usec >= 1_000_000 {
		t.Fatalf("TIME usec out of range: %d", usec)
	}
}

func TestLastSaveStartsAtServerStartup(t *testing.T) {
	before := time.Now().Unix()
	s := New(engine.New())

	got := execute(t, s, "LASTSAVE")
	if !strings.HasPrefix(got, ":") {
		t.Fatalf("LASTSAVE=%q", got)
	}
	value, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(got, ":"), "\r\n"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if value < before || value > time.Now().Unix()+1 {
		t.Fatalf("LASTSAVE startup value=%d", value)
	}
}

func TestObjectEncodingRefcountAndHelp(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "OBJECT", "ENCODING", "missing"); got != "$-1\r\n" {
		t.Fatalf("OBJECT ENCODING missing=%q", got)
	}
	if got := execute(t, s, "OBJECT", "REFCOUNT", "missing"); got != "$-1\r\n" {
		t.Fatalf("OBJECT REFCOUNT missing=%q", got)
	}

	execute(t, s, "SET", "key", "123")

	got := execute(t, s, "OBJECT", "ENCODING", "key")
	if got == "$-1\r\n" || !strings.HasPrefix(got, "$") {
		t.Fatalf("OBJECT ENCODING=%q", got)
	}
	if got := execute(t, s, "OBJECT", "REFCOUNT", "key"); got != ":1\r\n" {
		t.Fatalf("OBJECT REFCOUNT=%q", got)
	}

	got = execute(t, s, "OBJECT", "HELP")
	for _, want := range []string{"ENCODING", "IDLETIME", "FREQ", "REFCOUNT", "HELP"} {
		if !strings.Contains(got, want) {
			t.Fatalf("OBJECT HELP missing %q: %q", want, got)
		}
	}
}

func TestObjectTrackedMetadataDeferred(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "SET", "key", "value")

	for _, sub := range []string{"IDLETIME", "FREQ"} {
		raw := [][]byte{[]byte("OBJECT"), []byte(sub), []byte("key")}
		_, err := s.Execute(raw)
		if err == nil || !strings.Contains(err.Error(), "access metadata tracking") {
			t.Fatalf("OBJECT %s err=%v", sub, err)
		}
	}
}
