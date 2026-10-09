package server

import (
	"strings"
	"testing"
	"time"
)

func startBuiltinTestServer(t *testing.T) *atomicTestClient {
	t.Helper()
	tcp := startAtomicTestServer(t)
	if err := tcp.LoadBuiltinFunctions(); err != nil {
		t.Fatal(err)
	}
	return newAtomicTestClient(t, tcp)
}

func TestBuiltinLibraryLoadsAndReportsVersion(t *testing.T) {
	c := startBuiltinTestServer(t)
	if got := c.do("FCALL_RO", "snug_functions_version", "0"); got != "$1\r\n1\r\n" {
		t.Fatalf("version = %q", got)
	}
	list := c.do("FUNCTION", "LIST", "LIBRARYNAME", "snug")
	for _, name := range []string{"snug_rate_limit", "snug_lock_acquire", "snug_queue_pop", "snug_leaderboard_around"} {
		if !strings.Contains(list, name) {
			t.Fatalf("FUNCTION LIST lacks %s: %q", name, list)
		}
	}
	// Loading again replaces the library instead of failing.
	tcp := startAtomicTestServer(t)
	for i := 0; i < 2; i++ {
		if err := tcp.LoadBuiltinFunctions(); err != nil {
			t.Fatalf("load %d: %v", i, err)
		}
	}
}

func TestBuiltinRateLimiter(t *testing.T) {
	c := startBuiltinTestServer(t)
	// Capacity 3, refill 10 tokens per second.
	for i, want := range []string{"*3\r\n:1\r\n:2\r\n:0\r\n", "*3\r\n:1\r\n:1\r\n:0\r\n", "*3\r\n:1\r\n:0\r\n:0\r\n"} {
		if got := c.do("FCALL", "snug_rate_limit", "1", "rl", "3", "10"); got != want {
			t.Fatalf("call %d = %q want %q", i, got, want)
		}
	}
	got := c.do("FCALL", "snug_rate_limit", "1", "rl", "3", "10")
	if !strings.HasPrefix(got, "*3\r\n:0\r\n:0\r\n:") {
		t.Fatalf("limited call = %q", got)
	}
	time.Sleep(250 * time.Millisecond) // about 2.5 tokens
	if got := c.do("FCALL", "snug_rate_limit", "1", "rl", "3", "10", "2"); !strings.HasPrefix(got, "*3\r\n:1\r\n") {
		t.Fatalf("after refill = %q", got)
	}
	if got := c.do("FCALL", "snug_rate_limit", "1", "rl", "0", "10"); !strings.HasPrefix(got, "-ERR usage: snug_rate_limit") {
		t.Fatalf("bad arguments = %q", got)
	}
}

func TestBuiltinLockWithFencingToken(t *testing.T) {
	c := startBuiltinTestServer(t)
	if got := c.do("FCALL", "snug_lock_acquire", "2", "lock", "fence", "alice", "5000"); got != ":1\r\n" {
		t.Fatalf("alice acquire = %q", got)
	}
	if got := c.do("FCALL", "snug_lock_acquire", "2", "lock", "fence", "bob", "5000"); got != ":0\r\n" {
		t.Fatalf("bob acquire while held = %q", got)
	}
	if got := c.do("FCALL", "snug_lock_acquire", "2", "lock", "fence", "alice", "5000"); got != ":1\r\n" {
		t.Fatalf("alice re-acquire keeps the token = %q", got)
	}
	if got := c.do("FCALL", "snug_lock_renew", "1", "lock", "bob", "5000"); got != ":0\r\n" {
		t.Fatalf("bob renew = %q", got)
	}
	if got := c.do("FCALL", "snug_lock_release", "1", "lock", "bob"); got != ":0\r\n" {
		t.Fatalf("bob release = %q", got)
	}
	if got := c.do("FCALL", "snug_lock_renew", "1", "lock", "alice", "5000"); got != ":1\r\n" {
		t.Fatalf("alice renew = %q", got)
	}
	if got := c.do("FCALL", "snug_lock_release", "1", "lock", "alice"); got != ":1\r\n" {
		t.Fatalf("alice release = %q", got)
	}
	if got := c.do("FCALL", "snug_lock_acquire", "2", "lock", "fence", "bob", "50"); got != ":2\r\n" {
		t.Fatalf("bob acquire gets a larger token = %q", got)
	}
	time.Sleep(120 * time.Millisecond) // lock expires
	if got := c.do("FCALL", "snug_lock_acquire", "2", "lock", "fence", "carol", "5000"); got != ":3\r\n" {
		t.Fatalf("carol after expiry = %q", got)
	}
}

func TestBuiltinIdempotencyKeys(t *testing.T) {
	c := startBuiltinTestServer(t)
	if got := c.do("FCALL", "snug_idem_begin", "1", "pay:42", "5000"); got != "*1\r\n:1\r\n" {
		t.Fatalf("first begin = %q", got)
	}
	if got := c.do("FCALL", "snug_idem_begin", "1", "pay:42", "5000"); got != "*2\r\n:0\r\n$7\r\npending\r\n" {
		t.Fatalf("second begin = %q", got)
	}
	if got := c.do("FCALL", "snug_idem_commit", "1", "pay:42", "receipt-9", "5000"); got != ":1\r\n" {
		t.Fatalf("commit = %q", got)
	}
	if got := c.do("FCALL", "snug_idem_begin", "1", "pay:42", "5000"); got != "*3\r\n:0\r\n$4\r\ndone\r\n$9\r\nreceipt-9\r\n" {
		t.Fatalf("begin after commit = %q", got)
	}
	if got := c.do("FCALL", "snug_idem_commit", "1", "pay:42", "again", "5000"); got != ":0\r\n" {
		t.Fatalf("second commit = %q", got)
	}
	c.do("FCALL", "snug_idem_begin", "1", "pay:43", "5000")
	if got := c.do("FCALL", "snug_idem_abort", "1", "pay:43"); got != ":1\r\n" {
		t.Fatalf("abort = %q", got)
	}
	if got := c.do("FCALL", "snug_idem_begin", "1", "pay:43", "5000"); got != "*1\r\n:1\r\n" {
		t.Fatalf("begin after abort = %q", got)
	}
}

func TestBuiltinBoundedCounter(t *testing.T) {
	c := startBuiltinTestServer(t)
	for i, want := range []string{"*2\r\n:1\r\n:3\r\n", "*2\r\n:1\r\n:6\r\n", "*2\r\n:1\r\n:9\r\n", "*2\r\n:0\r\n:9\r\n"} {
		if got := c.do("FCALL", "snug_counter_add", "1", "stock", "3", "0", "10"); got != want {
			t.Fatalf("add %d = %q want %q", i, got, want)
		}
	}
	if got := c.do("FCALL", "snug_counter_add", "1", "stock", "-9", "0", "10"); got != "*2\r\n:1\r\n:0\r\n" {
		t.Fatalf("drain = %q", got)
	}
	if got := c.do("FCALL", "snug_counter_add", "1", "stock", "-1", "0", "10"); got != "*2\r\n:0\r\n:0\r\n" {
		t.Fatalf("below minimum = %q", got)
	}
	if got := c.do("FCALL", "snug_counter_add", "1", "stock", "1.5", "0", "10"); !strings.HasPrefix(got, "-ERR usage") {
		t.Fatalf("fraction = %q", got)
	}
}

func TestBuiltinReliableQueue(t *testing.T) {
	c := startBuiltinTestServer(t)
	keys := []string{"3", "q:ready", "q:inflight", "q:payloads"}
	call := func(fn string, extra ...string) string {
		args := append([]string{"FCALL", fn}, keys...)
		return c.do(append(args, extra...)...)
	}
	if got := call("snug_queue_push", "job-a"); got != ":1\r\n" {
		t.Fatalf("push a = %q", got)
	}
	if got := call("snug_queue_push", "job-b"); got != ":2\r\n" {
		t.Fatalf("push b = %q", got)
	}
	if got := call("snug_queue_pop", "5000"); got != "*2\r\n$1\r\n1\r\n$5\r\njob-a\r\n" {
		t.Fatalf("pop = %q", got)
	}
	if got := call("snug_queue_ack", "1"); got != ":1\r\n" {
		t.Fatalf("ack = %q", got)
	}
	if got := call("snug_queue_ack", "1"); got != ":0\r\n" {
		t.Fatalf("second ack = %q", got)
	}
	// job-b is delivered, never acknowledged, and comes back after its deadline.
	if got := call("snug_queue_pop", "60"); got != "*2\r\n$1\r\n2\r\n$5\r\njob-b\r\n" {
		t.Fatalf("pop b = %q", got)
	}
	if got := call("snug_queue_pop", "60"); got != "$-1\r\n" {
		t.Fatalf("pop empty = %q", got)
	}
	time.Sleep(120 * time.Millisecond)
	if got := call("snug_queue_pop", "5000"); got != "*2\r\n$1\r\n2\r\n$5\r\njob-b\r\n" {
		t.Fatalf("redelivery = %q", got)
	}
	if got := call("snug_queue_nack", "2"); got != ":1\r\n" {
		t.Fatalf("nack = %q", got)
	}
	if got := call("snug_queue_pop", "5000"); got != "*2\r\n$1\r\n2\r\n$5\r\njob-b\r\n" {
		t.Fatalf("pop after nack = %q", got)
	}
	if got := call("snug_queue_ack", "2"); got != ":1\r\n" {
		t.Fatalf("final ack = %q", got)
	}
	// Everything acknowledged: no payloads left behind except the sequence counter.
	if got := c.do("HLEN", "q:payloads"); got != ":1\r\n" {
		t.Fatalf("payload hash = %q", got)
	}
}

func TestBuiltinLeaderboard(t *testing.T) {
	c := startBuiltinTestServer(t)
	for _, entry := range [][2]string{{"ann", "50"}, {"bob", "70"}, {"cy", "60"}, {"di", "80"}, {"ed", "40"}} {
		c.do("FCALL", "snug_leaderboard_submit", "1", "lb", entry[0], entry[1])
	}
	// Default mode keeps the better score.
	if got := c.do("FCALL", "snug_leaderboard_submit", "1", "lb", "bob", "10"); got != "*2\r\n:1\r\n$2\r\n70\r\n" {
		t.Fatalf("lower score = %q", got)
	}
	if got := c.do("FCALL", "snug_leaderboard_submit", "1", "lb", "bob", "75"); got != "*2\r\n:1\r\n$2\r\n75\r\n" {
		t.Fatalf("higher score = %q", got)
	}
	if got := c.do("FCALL", "snug_leaderboard_submit", "1", "lb", "bob", "20", "replace"); !strings.HasPrefix(got, "*2\r\n:4\r\n") {
		t.Fatalf("replace = %q", got)
	}
	got := c.do("FCALL_RO", "snug_leaderboard_around", "1", "lb", "cy", "1")
	want := "*3\r\n:1\r\n$2\r\n60\r\n*4\r\n$2\r\ndi\r\n$2\r\n80\r\n$2\r\ncy\r\n$2\r\n60\r\n"
	if !strings.HasPrefix(got, "*3\r\n:1\r\n$2\r\n60\r\n*6\r\n$2\r\ndi\r\n$2\r\n80\r\n$2\r\ncy\r\n$2\r\n60\r\n$3\r\nann\r\n") && got != want {
		t.Fatalf("around = %q", got)
	}
	if got := c.do("FCALL_RO", "snug_leaderboard_around", "1", "lb", "nobody", "2"); got != "*3\r\n$-1\r\n$-1\r\n*0\r\n" {
		t.Fatalf("unknown member = %q", got)
	}
}
