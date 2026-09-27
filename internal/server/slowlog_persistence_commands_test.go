package server

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"snugkv/internal/engine"
	"snugkv/internal/persistence"
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

func TestPersistenceRedis810ArgumentErrors(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"SAVE", "extra"}, "ERR wrong number of arguments for 'save' command"},
		{[]string{"LASTSAVE", "extra"}, "ERR wrong number of arguments for 'lastsave' command"},
		{[]string{"BGSAVE", "invalid"}, "ERR syntax error"},
		{[]string{"BGSAVE", "SCHEDULE", "extra"}, "ERR syntax error"},
		{[]string{"BGREWRITEAOF", "extra"}, "ERR wrong number of arguments for 'bgrewriteaof' command"},
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

func TestBGRewriteAOFWithoutJournaling(t *testing.T) {
	s := New(engine.New())
	s.aofRewritePath = filepath.Join(t.TempDir(), "export.aof")
	execute(t, s, "SET", "before", "value")
	if got := execute(t, s, "BGREWRITEAOF"); got != "+Background append only file rewriting started\r\n" {
		t.Fatalf("BGREWRITEAOF=%q", got)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.persistenceJobMu.Lock()
		running := s.aofRewriteRunning
		s.persistenceJobMu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rewrite did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	found, reset := false, false
	if err := persistence.Replay(s.aofRewritePath, func(records []persistence.Record) error {
		for _, record := range records {
			reset = reset || record.Reset
			if string(record.Key) == "before" && string(record.Value) == "value" {
				found = true
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !found || !reset {
		t.Fatalf("rewrite replay: found=%v reset=%v", found, reset)
	}
	before, err := os.ReadFile(s.aofRewritePath)
	if err != nil {
		t.Fatal(err)
	}
	execute(t, s, "SET", "after", "not-journaled")
	after, err := os.ReadFile(s.aofRewritePath)
	if err != nil {
		t.Fatal(err)
	}
	if s.journal != nil || s.configAppendOnly || !bytes.Equal(before, after) {
		t.Fatal("one-off rewrite enabled journaling or changed after a later SET")
	}
}

func TestBGRewriteAOFInvalidDestination(t *testing.T) {
	s := New(engine.New())
	s.aofRewritePath = filepath.Join(t.TempDir(), "missing", "export.aof")
	_, err := s.Execute(clientArgs("BGREWRITEAOF"))
	if err == nil || err.Error() != "ERR cannot open AOF rewrite destination" {
		t.Fatalf("error=%v", err)
	}
	if s.aofRewriteRunning || s.journal != nil {
		t.Fatal("failed rewrite left active state")
	}
}


func TestSlowlogRedis82TrimsArguments(t *testing.T) {
	args := make([][]byte, 0, 34)
	args = append(args, []byte("SADD"), []byte("set"))
	for i := 3; i <= 34; i++ {
		args = append(args, []byte(strconv.Itoa(i)))
	}
	got := slowlogSanitizeArgs(args)
	if len(got) != 32 {
		t.Fatalf("argc=%d, want 32", len(got))
	}
	if string(got[31]) != "... (3 more arguments)" {
		t.Fatalf("tail=%q", got[31])
	}

	long := bytes.Repeat([]byte("A"), 129)
	got = slowlogSanitizeArgs([][]byte{[]byte("SADD"), []byte("set"), []byte("foo"), long})
	want := strings.Repeat("A", 128) + "... (1 more bytes)"
	if string(got[3]) != want {
		t.Fatalf("long arg=%q, want %q", got[3], want)
	}
}

func TestSlowlogRedis82RedactsSupportedSensitiveArgs(t *testing.T) {
	cases := []struct {
		args [][]byte
		want []string
	}{
		{
			args: clientArgs("ACL", "SETUSER", "alice", ">secret", "+get"),
			want: []string{"ACL", "SETUSER", "(redacted)", "(redacted)", "(redacted)"},
		},
		{
			args: clientArgs("ACL", "GETUSER", "alice"),
			want: []string{"ACL", "GETUSER", "(redacted)"},
		},
		{
			args: clientArgs("ACL", "DELUSER", "alice", "bob"),
			want: []string{"ACL", "DELUSER", "(redacted)", "(redacted)"},
		},
		{
			args: clientArgs("MIGRATE", "127.0.0.1", "6379", "k", "0", "5000", "AUTH", "secret"),
			want: []string{"MIGRATE", "127.0.0.1", "6379", "k", "0", "5000", "AUTH", "(redacted)"},
		},
		{
			args: clientArgs("MIGRATE", "127.0.0.1", "6379", "k", "0", "5000", "AUTH2", "alice", "secret"),
			want: []string{"MIGRATE", "127.0.0.1", "6379", "k", "0", "5000", "AUTH2", "(redacted)", "(redacted)"},
		},
	}
	for _, tc := range cases {
		got := slowlogSanitizeArgs(tc.args)
		if len(got) != len(tc.want) {
			t.Fatalf("%q argc=%d want=%d", tc.args[0], len(got), len(tc.want))
		}
		for i := range tc.want {
			if string(got[i]) != tc.want[i] {
				t.Fatalf("%q arg %d=%q want=%q", tc.args[0], i, got[i], tc.want[i])
			}
		}
	}
}


func TestSlowlogRecordedOutputUsesTrimmingAndRedaction(t *testing.T) {
	tcp, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	s := tcp.server

	execute(t, s, "CONFIG", "SET", "slowlog-log-slower-than", "0")
	execute(t, s, "SLOWLOG", "RESET")

	conn, err := net.DialTimeout("tcp", tcp.listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	wire := "*5\r\n" +
		"$3\r\nACL\r\n" +
		"$7\r\nSETUSER\r\n" +
		"$12\r\nslowlog-user\r\n" +
		"$12\r\n>supersecret\r\n" +
		"$4\r\n+get\r\n"
	if _, err := io.WriteString(conn, wire); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len("+OK\r\n"))
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply) != "+OK\r\n" {
		t.Fatalf("ACL SETUSER reply=%q", reply)
	}

	long := bytes.Repeat([]byte("A"), 129)
	s.recordSlowlogForClient(nil, [][]byte{[]byte("SADD"), []byte("slowlog:set"), []byte("foo"), long}, 0)

	many := make([][]byte, 0, 34)
	many = append(many, []byte("SADD"), []byte("slowlog:set"))
	for i := 3; i <= 34; i++ {
		many = append(many, []byte(strconv.Itoa(i)))
	}
	s.recordSlowlogForClient(nil, many, 0)

	got := execute(t, s, "SLOWLOG", "GET", "-1")
	for _, want := range []string{
		"(redacted)",
		strings.Repeat("A", 128) + "... (1 more bytes)",
		"... (3 more arguments)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("SLOWLOG GET missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "supersecret") || strings.Contains(got, "slowlog-user") {
		t.Fatalf("SLOWLOG leaked sensitive ACL payload: %q", got)
	}
}
