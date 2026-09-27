package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func TestSlowlogGetLenResetAndConfig(t *testing.T) {
	s := New(engine.New())

	if got := execute(t, s, "CONFIG", "SET", "slowlog-log-slower-than", "0"); got != "+OK\r\n" {
		t.Fatalf("CONFIG SET threshold=%q", got)
	}
	if got := execute(t, s, "CONFIG", "SET", "slowlog-max-len", "2"); got != "+OK\r\n" {
		t.Fatalf("CONFIG SET max len=%q", got)
	}

	execute(t, s, "SET", "a", "1")
	execute(t, s, "GET", "a")
	execute(t, s, "PING")

	got := execute(t, s, "SLOWLOG", "LEN")
	if got != ":2\r\n" {
		t.Fatalf("SLOWLOG LEN=%q", got)
	}

	got = execute(t, s, "SLOWLOG", "GET", "1")
	if !strings.HasPrefix(got, "*1\r\n") || !strings.Contains(got, "PING") {
		t.Fatalf("SLOWLOG GET=%q", got)
	}

	if got := execute(t, s, "SLOWLOG", "RESET"); got != "+OK\r\n" {
		t.Fatalf("SLOWLOG RESET=%q", got)
	}
	if got := execute(t, s, "SLOWLOG", "LEN"); got != ":0\r\n" {
		t.Fatalf("SLOWLOG LEN after reset=%q", got)
	}

	got = execute(t, s, "CONFIG", "GET", "slowlog-*")
	if !strings.Contains(got, "slowlog-log-slower-than") || !strings.Contains(got, "slowlog-max-len") {
		t.Fatalf("CONFIG GET slowlog=%q", got)
	}
}

func TestSaveAndLastSave(t *testing.T) {
	s := New(engine.New())
	dir := t.TempDir()
	s.snapshotPath = filepath.Join(dir, "dump.rdb")

	execute(t, s, "SET", "k", "v")
	before := s.lastSaveUnix.Load()

	if got := execute(t, s, "SAVE"); got != "+OK\r\n" {
		t.Fatalf("SAVE=%q", got)
	}
	if _, err := os.Stat(s.snapshotPath); err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
	if s.lastSaveUnix.Load() < before {
		t.Fatalf("LASTSAVE moved backwards")
	}
}

func TestSaveDisabled(t *testing.T) {
	s := New(engine.New())
	raw := [][]byte{[]byte("SAVE")}
	_, err := s.Execute(raw)
	if err == nil || !strings.Contains(err.Error(), "snapshot persistence is disabled") {
		t.Fatalf("SAVE disabled err=%v", err)
	}
}

func TestBGSave(t *testing.T) {
	s := New(engine.New())
	dir := t.TempDir()
	s.snapshotPath = filepath.Join(dir, "dump.rdb")
	execute(t, s, "SET", "k", "v")

	got := execute(t, s, "BGSAVE")
	if !strings.Contains(got, "Background saving started") {
		t.Fatalf("BGSAVE=%q", got)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(s.snapshotPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("BGSAVE did not create snapshot")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBGRewriteAOFDisabled(t *testing.T) {
	s := New(engine.New())
	raw := [][]byte{[]byte("BGREWRITEAOF")}
	_, err := s.Execute(raw)
	if err == nil || !strings.Contains(err.Error(), "AOF is disabled") {
		t.Fatalf("BGREWRITEAOF disabled err=%v", err)
	}
}

func TestSlowlogHelp(t *testing.T) {
	s := New(engine.New())
	got := execute(t, s, "SLOWLOG", "HELP")
	for _, want := range []string{"GET", "LEN", "RESET", "HELP"} {
		if !strings.Contains(got, want) {
			t.Fatalf("SLOWLOG HELP missing %q: %q", want, got)
		}
	}
}
