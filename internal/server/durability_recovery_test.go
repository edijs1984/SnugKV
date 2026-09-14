package server

import (
	"os"
	"path/filepath"
	"snugkv/internal/engine"
	"snugkv/internal/persistence"
	"testing"
)

func TestAOFRestartRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.snug")

	first, err := engine.NewWithOptions(engine.Options{
		Shards:   16,
		Encoding: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	journal, err := persistence.Open(path, "always")
	if err != nil {
		t.Fatal(err)
	}

	srv := New(first)
	srv.SetJournal(journal)

	execute := func(args ...string) []byte {
		t.Helper()

		raw := make([][]byte, len(args))
		for i := range args {
			raw[i] = []byte(args[i])
		}

		response, err := srv.Execute(raw)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}

		return response
	}

	if got := string(execute("SET", "plain", "hello")); got != "+OK\r\n" {
		t.Fatalf("SET plain = %q", got)
	}

	// Exercise semantic encoding as well as ordinary strings.
	if got := string(execute("SET", "number", "9223372036854775808")); got != "+OK\r\n" {
		t.Fatalf("SET number = %q", got)
	}

	if got := string(execute("SET", "temporary", "alive", "PX", "60000")); got != "+OK\r\n" {
		t.Fatalf("SET temporary = %q", got)
	}

	if got := string(execute("SET", "deleted", "remove-me")); got != "+OK\r\n" {
		t.Fatalf("SET deleted = %q", got)
	}

	if got := string(execute("DEL", "deleted")); got != ":1\r\n" {
		t.Fatalf("DEL = %q", got)
	}

	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate a complete process restart: a fresh engine with no in-memory
	// state, then the same startup recovery path used by cmd/snugkv.
	restarted, err := engine.NewWithOptions(engine.Options{
		Shards:   16,
		Encoding: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	reopened, err := persistence.Open(path, "always")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	if err := persistence.Replay(path, func(records []persistence.Record) error {
		return restarted.Restore(records, false)
	}); err != nil {
		t.Fatal(err)
	}

	if got, ok := restarted.Get("plain"); !ok || string(got) != "hello" {
		t.Fatalf("plain after restart = %q %v", got, ok)
	}

	if got, ok := restarted.Get("number"); !ok || string(got) != "9223372036854775808" {
		t.Fatalf("number after restart = %q %v", got, ok)
	}

	if got, ok := restarted.Get("temporary"); !ok || string(got) != "alive" {
		t.Fatalf("temporary after restart = %q %v", got, ok)
	}

	ttl := restarted.TTL("temporary", true)
	if ttl <= 0 || ttl > 60000 {
		t.Fatalf("temporary TTL after restart = %d", ttl)
	}

	if got, ok := restarted.Get("deleted"); ok {
		t.Fatalf("deleted key recovered: %q", got)
	}

	// Recovery must not damage the database's ability to accept new writes.
	if err := restarted.Set("after-restart", []byte("works"), 0); err != nil {
		t.Fatal(err)
	}

	if got, ok := restarted.Get("after-restart"); !ok || string(got) != "works" {
		t.Fatalf("post-restart write = %q %v", got, ok)
	}
}

func TestAOFRestartIgnoresTruncatedFinalFrame(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.snug")

	store := engine.New()

	journal, err := persistence.Open(path, "always")
	if err != nil {
		t.Fatal(err)
	}

	srv := New(store)
	srv.SetJournal(journal)

	execute := func(args ...string) {
		t.Helper()

		raw := make([][]byte, len(args))
		for i := range args {
			raw[i] = []byte(args[i])
		}

		if _, err := srv.Execute(raw); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}

	execute("SET", "survives", "first")

	// Record the exact boundary of the first complete durable frame.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	firstBoundary := info.Size()

	execute("SET", "interrupted", "second")

	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= firstBoundary+4 {
		t.Fatalf(
			"AOF unexpectedly small: first=%d final=%d",
			firstBoundary,
			info.Size(),
		)
	}

	// Simulate a process/power failure while the final frame header is being
	// written. The first frame is complete; the second is incomplete.
	if err := os.Truncate(path, firstBoundary+4); err != nil {
		t.Fatal(err)
	}

	// This matches startup behavior: Open validates the history and truncates
	// an incomplete final frame back to the last complete frame.
	reopened, err := persistence.Open(path, "always")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	afterOpen, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if afterOpen.Size() != firstBoundary {
		t.Fatalf(
			"AOF was not repaired to last complete frame: got=%d want=%d",
			afterOpen.Size(),
			firstBoundary,
		)
	}

	restarted := engine.New()

	if err := persistence.Replay(path, func(records []persistence.Record) error {
		return restarted.Restore(records, false)
	}); err != nil {
		t.Fatal(err)
	}

	if got, ok := restarted.Get("survives"); !ok || string(got) != "first" {
		t.Fatalf("complete frame lost: %q %v", got, ok)
	}

	if got, ok := restarted.Get("interrupted"); ok {
		t.Fatalf("incomplete frame recovered: %q", got)
	}

	if err := restarted.Set("after-recovery", []byte("works"), 0); err != nil {
		t.Fatal(err)
	}
}

func TestAOFRestartRejectsChecksumCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.snug")

	store := engine.New()

	journal, err := persistence.Open(path, "always")
	if err != nil {
		t.Fatal(err)
	}

	srv := New(store)
	srv.SetJournal(journal)

	if _, err := srv.Execute([][]byte{
		[]byte("SET"),
		[]byte("important"),
		[]byte("value"),
	}); err != nil {
		t.Fatal(err)
	}

	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(data) <= 16 {
		t.Fatalf("AOF unexpectedly short: %d", len(data))
	}

	// Corrupt persisted payload while leaving the frame structurally present.
	// CRC validation must make startup fail rather than accepting bad state.
	data[len(data)-1] ^= 0x01

	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	if reopened, err := persistence.Open(path, "always"); err == nil {
		reopened.Close()
		t.Fatal("corrupted AOF was accepted")
	}
}
