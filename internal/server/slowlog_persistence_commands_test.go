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
	if !strings.HasPrefix(got, "*1\r\n") || !strings.Contains(got, "LEN") {
		t.Fatalf("SLOWLOG GET=%q", got)
	}

	if got := execute(t, s, "SLOWLOG", "RESET"); got != "+OK\r\n" {
		t.Fatalf("SLOWLOG RESET=%q", got)
	}
	if got := execute(t, s, "SLOWLOG", "LEN"); got != ":1\r\n" {
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

// Redis 8.10.2 live oracle results, captured on 2026-09-27.
func TestSlowlogRedis810Errors(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"SLOWLOG"}, "ERR wrong number of arguments for 'slowlog' command"},
		{[]string{"SLOWLOG", "UNKNOWN"}, "ERR unknown subcommand 'UNKNOWN'. Try SLOWLOG HELP."},
		{[]string{"SLOWLOG", "GET", "invalid"}, "ERR count should be greater than or equal to -1"},
		{[]string{"SLOWLOG", "GET", "-2"}, "ERR count should be greater than or equal to -1"},
		{[]string{"SLOWLOG", "GET", "9223372036854775808"}, "ERR count should be greater than or equal to -1"},
		{[]string{"SLOWLOG", "GET", "1", "extra"}, "ERR unknown subcommand or wrong number of arguments for 'GET'. Try SLOWLOG HELP."},
		{[]string{"SLOWLOG", "LEN", "extra"}, "ERR wrong number of arguments for 'slowlog|len' command"},
		{[]string{"SLOWLOG", "RESET", "extra"}, "ERR wrong number of arguments for 'slowlog|reset' command"},
		{[]string{"SLOWLOG", "HELP", "extra"}, "ERR wrong number of arguments for 'slowlog|help' command"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			s := New(engine.New())
			args := make([][]byte, len(tc.args))
			for i, arg := range tc.args {
				args[i] = []byte(arg)
			}
			_, err := s.Execute(args)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestSlowlogRedis810CountBounds(t *testing.T) {
	s := New(engine.New())
	s.slowlogEntries = []slowlogEntry{
		{id: 2, args: [][]byte{[]byte("PING")}},
		{id: 1, args: [][]byte{[]byte("GET"), []byte("k")}},
	}
	for _, tc := range []struct {
		count  string
		prefix string
	}{
		{"0", "*0\r\n"},
		{"1", "*1\r\n"},
		{"-1", "*2\r\n"},
		{"9223372036854775807", "*2\r\n"},
	} {
		got := execute(t, s, "SLOWLOG", "GET", tc.count)
		if !strings.HasPrefix(got, tc.prefix) {
			t.Fatalf("GET %s=%q, want prefix %q", tc.count, got, tc.prefix)
		}
	}
}

func TestSlowlogRedis810HelpExact(t *testing.T) {
	s := New(engine.New())
	want := "*12\r\n$64\r\nSLOWLOG <subcommand> [<arg> [value] [opt] ...]. Subcommands are:\r\n$13\r\nGET [<count>]\r\n$75\r\n    Return top <count> entries from the slowlog (default: 10, -1 mean all).\r\n$24\r\n    Entries are made of:\r\n$77\r\n    id, timestamp, time in microseconds, arguments array, client IP and port,\r\n$15\r\n    client name\r\n$3\r\nLEN\r\n$37\r\n    Return the length of the slowlog.\r\n$5\r\nRESET\r\n$22\r\n    Reset the slowlog.\r\n$4\r\nHELP\r\n$20\r\n    Print this help.\r\n"
	if got := execute(t, s, "SLOWLOG", "HELP"); got != want {
		t.Fatalf("HELP=%q, want %q", got, want)
	}
}

func TestSlowlogRedis810LogsOwnCommands(t *testing.T) {
	s := New(engine.New())
	execute(t, s, "CONFIG", "SET", "slowlog-log-slower-than", "0")
	execute(t, s, "SLOWLOG", "RESET")
	execute(t, s, "PING")
	if got := execute(t, s, "SLOWLOG", "LEN"); got != ":2\r\n" {
		t.Fatalf("LEN before recording itself=%q", got)
	}
	got := execute(t, s, "SLOWLOG", "GET", "-1")
	if !strings.HasPrefix(got, "*3\r\n") {
		t.Fatalf("GET must return RESET, PING, LEN before recording itself: %q", got)
	}
	// GET was appended after producing its response. IDs start at zero
	// and RESET clears entries without restarting the ID sequence.
	for i, want := range []string{"SLOWLOG GET -1", "SLOWLOG LEN", "PING", "SLOWLOG RESET"} {
		entry := s.slowlogEntries[i]
		parts := make([]string, len(entry.args))
		for j, arg := range entry.args {
			parts[j] = string(arg)
		}
		command := strings.Join(parts, " ")
		if command != want || entry.id != int64(4-i) {
			t.Fatalf("entry %d: command=%q id=%d, want %q id=%d", i, command, entry.id, want, 4-i)
		}
	}
	execute(t, s, "SLOWLOG", "RESET")
	if got := execute(t, s, "SLOWLOG", "LEN"); got != ":1\r\n" {
		t.Fatalf("RESET must log itself: LEN=%q", got)
	}
	execute(t, s, "CONFIG", "SET", "slowlog-log-slower-than", "-1")
	execute(t, s, "SLOWLOG", "RESET")
	if got := execute(t, s, "SLOWLOG", "LEN"); got != ":0\r\n" {
		t.Fatalf("disabled logging: LEN=%q", got)
	}
}
