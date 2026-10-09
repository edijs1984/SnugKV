package server

import (
	"bufio"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"snugkv/internal/config"
	"snugkv/internal/engine"
	"snugkv/internal/persistence"
)

type atomicTestClient struct {
	conn net.Conn
	r    *bufio.Reader
	t    *testing.T
}

func newAtomicTestClient(t *testing.T, tcp *TCPServer) *atomicTestClient {
	t.Helper()
	conn, err := net.Dial("tcp", tcp.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &atomicTestClient{conn: conn, r: bufio.NewReader(conn), t: t}
}

func (c *atomicTestClient) do(args ...string) string {
	c.t.Helper()
	return txCommand(c.t, c.conn, c.r, args...)
}

func startAtomicTestServer(t *testing.T) *TCPServer {
	t.Helper()
	tcp, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tcp.Close() })
	return tcp
}

func TestAtomicTransactionRollsBackEarlierWritesOnFailure(t *testing.T) {
	tcp := startAtomicTestServer(t)
	c := newAtomicTestClient(t, tcp)

	c.do("SET", "keep", "old")
	c.do("MULTI", "ATOMIC")
	c.do("SET", "keep", "new")
	c.do("SET", "fresh", "1")
	c.do("INCR", "keep") // "new" is not an integer
	c.do("SET", "never", "1")
	got := c.do("EXEC")
	if !strings.HasPrefix(got, "-EXECABORT Atomic transaction rolled back: command 3 (INCR) failed") {
		t.Fatalf("EXEC = %q", got)
	}
	if got := c.do("GET", "keep"); got != "$3\r\nold\r\n" {
		t.Fatalf("keep = %q", got)
	}
	if got := c.do("EXISTS", "fresh"); got != ":0\r\n" {
		t.Fatalf("fresh = %q", got)
	}
	if got := c.do("EXISTS", "never"); got != ":0\r\n" {
		t.Fatalf("never = %q", got)
	}
}

func TestAtomicTransactionCommitsWhenEveryCommandSucceeds(t *testing.T) {
	tcp := startAtomicTestServer(t)
	c := newAtomicTestClient(t, tcp)

	c.do("MULTI", "ATOMIC")
	c.do("SET", "n", "5")
	c.do("INCR", "n")
	c.do("RPUSH", "l", "a", "b")
	if got := c.do("EXEC"); got != "*3\r\n+OK\r\n:6\r\n:2\r\n" {
		t.Fatalf("EXEC = %q", got)
	}
	if got := c.do("GET", "n"); got != "$1\r\n6\r\n" {
		t.Fatalf("n = %q", got)
	}
	// The connection is back to normal mode afterwards.
	c.do("MULTI")
	c.do("SET", "plain", "1")
	c.do("LPUSH", "plain", "x")
	got := c.do("EXEC")
	if !strings.HasPrefix(got, "*2\r\n+OK\r\n-WRONGTYPE") {
		t.Fatalf("plain MULTI EXEC = %q", got)
	}
	if got := c.do("GET", "plain"); got != "$1\r\n1\r\n" {
		t.Fatalf("plain MULTI must keep partial effects, got %q", got)
	}
}

func TestAtomicTransactionRejectsSideEffectCommandsAtQueueTime(t *testing.T) {
	tcp := startAtomicTestServer(t)
	c := newAtomicTestClient(t, tcp)

	c.do("MULTI", "ATOMIC")
	c.do("SET", "a", "1")
	if got := c.do("PUBLISH", "chan", "hello"); !strings.HasPrefix(got, "-ERR command 'publish' is not allowed inside MULTI ATOMIC") {
		t.Fatalf("PUBLISH = %q", got)
	}
	if got := c.do("EXEC"); !strings.HasPrefix(got, "-EXECABORT") {
		t.Fatalf("EXEC = %q", got)
	}
	if got := c.do("EXISTS", "a"); got != ":0\r\n" {
		t.Fatalf("a = %q", got)
	}
}

func TestMultiRejectsUnknownOption(t *testing.T) {
	tcp := startAtomicTestServer(t)
	c := newAtomicTestClient(t, tcp)
	if got := c.do("MULTI", "FAST"); !strings.HasPrefix(got, "-ERR wrong number of arguments for 'multi'") {
		t.Fatalf("MULTI FAST = %q", got)
	}
	if got := c.do("MULTI", "atomic"); got != "+OK\r\n" {
		t.Fatalf("MULTI atomic = %q", got)
	}
	if got := c.do("MULTI", "ATOMIC"); !strings.HasPrefix(got, "-ERR MULTI calls can not be nested") {
		t.Fatalf("nested = %q", got)
	}
	if got := c.do("DISCARD"); got != "+OK\r\n" {
		t.Fatalf("DISCARD = %q", got)
	}
}

func TestAtomicTransactionWorksWithWatch(t *testing.T) {
	tcp := startAtomicTestServer(t)
	a := newAtomicTestClient(t, tcp)
	b := newAtomicTestClient(t, tcp)

	a.do("SET", "w", "1")
	a.do("WATCH", "w")
	b.do("SET", "w", "2")
	a.do("MULTI", "ATOMIC")
	a.do("SET", "w", "3")
	if got := a.do("EXEC"); got != "*-1\r\n" {
		t.Fatalf("EXEC after conflicting write = %q", got)
	}
	if got := a.do("GET", "w"); got != "$1\r\n2\r\n" {
		t.Fatalf("w = %q", got)
	}
}

// Every write command type must be restored exactly. The corpus mutates keys of
// every data type; a second, failing command aborts the transaction, and the
// whole database must then be identical to its state before the transaction.
func TestAtomicRollbackRestoresEveryWriteCommandExactly(t *testing.T) {
	setup := [][]string{
		{"SET", "str", "hello"},
		{"SET", "num", "10"},
		{"SET", "ttl", "alive", "EX", "3000"},
		{"HSET", "hash", "f1", "v1", "f2", "v2"},
		{"RPUSH", "list", "a", "b", "c"},
		{"SADD", "set", "x", "y", "z"},
		{"SADD", "set2", "y", "q"},
		{"ZADD", "zset", "1", "one", "2", "two", "3", "three"},
		{"ZADD", "zset2", "5", "two", "6", "six"},
		{"XADD", "stream", "1-1", "k", "v"},
		{"SETBIT", "bits", "7", "1"},
		{"PFADD", "hll", "a", "b"},
		{"GEOADD", "geo", "13.361389", "38.115556", "Palermo"},
		{"JSON.SET", "doc", "$", `{"a":1,"b":[1,2]}`},
	}
	corpus := [][]string{
		{"SET", "str", "changed"},
		{"SET", "brand-new", "1"},
		{"SET", "ttl", "other", "KEEPTTL"},
		{"SET", "ttl", "other"},
		{"APPEND", "str", "!!"},
		{"SETRANGE", "str", "1", "EE"},
		{"INCR", "num"},
		{"INCRBY", "num", "5"},
		{"GETDEL", "str"},
		{"DEL", "str", "hash", "list"},
		{"UNLINK", "set"},
		{"EXPIRE", "str", "100"},
		{"PERSIST", "ttl"},
		{"RENAME", "str", "renamed"},
		{"COPY", "str", "copied"},
		{"HSET", "hash", "f1", "other", "f3", "v3"},
		{"HDEL", "hash", "f1"},
		{"HINCRBY", "hash", "counter", "4"},
		{"LPUSH", "list", "z"},
		{"RPOP", "list"},
		{"LTRIM", "list", "0", "0"},
		{"LSET", "list", "0", "replaced"},
		{"LMOVE", "list", "list2", "LEFT", "RIGHT"},
		{"SADD", "set", "new"},
		{"SREM", "set", "x"},
		{"SMOVE", "set", "set2", "y"},
		{"SINTERSTORE", "dst", "set", "set2"},
		{"SUNIONSTORE", "dst", "set", "set2"},
		{"ZADD", "zset", "9", "nine"},
		{"ZREM", "zset", "one"},
		{"ZINCRBY", "zset", "5", "two"},
		{"ZUNIONSTORE", "zdst", "2", "zset", "zset2"},
		{"ZINTERSTORE", "zdst", "2", "zset", "zset2"},
		{"ZPOPMIN", "zset"},
		{"XADD", "stream", "2-1", "k", "v2"},
		{"XDEL", "stream", "1-1"},
		{"XGROUP", "CREATE", "stream", "g", "0"},
		{"SETBIT", "bits", "100", "1"},
		{"PFADD", "hll", "c", "d", "e"},
		{"PFMERGE", "hll2", "hll"},
		{"GEOADD", "geo", "15.087269", "37.502669", "Catania"},
		{"JSON.SET", "doc", "$.a", "2"},
		{"JSON.DEL", "doc", "$.b"},
		{"FLUSHALL"},
		{"EVAL", "redis.call('SET','scripted','1'); redis.call('DEL','hash'); return 1", "0"},
	}
	for _, cmd := range corpus {
		cmd := cmd
		t.Run(strings.Join(cmd[:1], ""), func(t *testing.T) {
			tcp := startAtomicTestServer(t)
			c := newAtomicTestClient(t, tcp)
			for _, s := range setup {
				if got := c.do(s...); strings.HasPrefix(got, "-") {
					t.Fatalf("setup %v = %q", s, got)
				}
			}
			before := tcp.server.store.Export(nil)

			c.do("MULTI", "ATOMIC")
			if got := c.do(cmd...); got != "+QUEUED\r\n" {
				t.Fatalf("queue %v = %q", cmd, got)
			}
			// SET with EX 0 is rejected at run time whatever the corpus command did.
			if got := c.do("SET", "probe-key", "v", "EX", "0"); got != "+QUEUED\r\n" {
				t.Fatalf("queue probe = %q", got)
			}
			got := c.do("EXEC")
			if !strings.HasPrefix(got, "-EXECABORT Atomic transaction rolled back: command 2 (SET)") {
				t.Fatalf("EXEC %v = %q", cmd, got)
			}
			after := tcp.server.store.Export(nil)
			if diff := persistenceDiff(before, after); len(diff) != 0 {
				keys := make([]string, 0, len(diff))
				for _, record := range diff {
					keys = append(keys, string(record.Key))
				}
				t.Fatalf("%v was not rolled back; differing keys: %v (EXEC = %q)", cmd, keys, got)
			}
		})
	}
}

func TestAtomicRollbackLeavesNothingInTheAOFAndCommitIsRecovered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.snug")
	store := engine.New()
	journal, err := persistence.Open(path, "always")
	if err != nil {
		t.Fatal(err)
	}
	c0 := config.Default()
	c0.ListenAddr = "127.0.0.1:0"
	tcp, err := ListenWithJournal(c0, store, journal)
	if err != nil {
		t.Fatal(err)
	}
	c := newAtomicTestClient(t, tcp)

	c.do("SET", "base", "1")
	c.do("MULTI", "ATOMIC")
	c.do("SET", "base", "2")
	c.do("SET", "ghost", "1")
	c.do("LPUSH", "base", "x")
	if got := c.do("EXEC"); !strings.HasPrefix(got, "-EXECABORT") {
		t.Fatalf("EXEC = %q", got)
	}
	c.do("MULTI", "ATOMIC")
	c.do("SET", "committed", "yes")
	c.do("INCR", "base")
	if got := c.do("EXEC"); got != "*2\r\n+OK\r\n:2\r\n" {
		t.Fatalf("EXEC = %q", got)
	}
	tcp.Close()

	restarted := engine.New()
	if err := persistence.Replay(path, func(records []persistence.Record) error {
		return restarted.Restore(records, false)
	}); err != nil {
		t.Fatal(err)
	}
	if got, ok := restarted.Get("base"); !ok || string(got) != "2" {
		t.Fatalf("base after restart = %q %v", got, ok)
	}
	if got, ok := restarted.Get("committed"); !ok || string(got) != "yes" {
		t.Fatalf("committed after restart = %q %v", got, ok)
	}
	if _, ok := restarted.Get("ghost"); ok {
		t.Fatal("rolled-back write reached the AOF")
	}
}

// No other connection may ever observe the intermediate state of a transaction
// that is later rolled back.
func TestAtomicRolledBackWritesAreNeverVisibleToOtherClients(t *testing.T) {
	tcp := startAtomicTestServer(t)
	writer := newAtomicTestClient(t, tcp)
	reader := newAtomicTestClient(t, tcp)
	writer.do("SET", "k", "stable")
	writer.do("SET", "n", "x")

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			writer.do("MULTI", "ATOMIC")
			writer.do("SET", "k", "dirty")
			writer.do("INCR", "n")
			writer.do("EXEC")
		}
		close(stop)
	}()
	for {
		select {
		case <-stop:
			wg.Wait()
			return
		default:
		}
		if got := reader.do("GET", "k"); got != "$6\r\nstable\r\n" {
			t.Fatalf("reader saw %q", got)
		}
	}
}

func luaArgs(script string) []string { return []string{"EVAL", script, "0"} }

func TestAtomicScriptRollsBackOnlyWhenFlagged(t *testing.T) {
	tcp := startAtomicTestServer(t)
	c := newAtomicTestClient(t, tcp)

	body := "redis.call('SET','a','1'); redis.call('LPUSH','a','x'); return 1"
	if got := c.do(luaArgs(body)...); !strings.HasPrefix(got, "-") {
		t.Fatalf("plain script = %q", got)
	}
	// Redis semantics: the write before the failure stays.
	if got := c.do("GET", "a"); got != "$1\r\n1\r\n" {
		t.Fatalf("plain script kept write = %q", got)
	}

	c.do("DEL", "a")
	if got := c.do(luaArgs("#!lua flags=atomic\n" + body)...); !strings.HasPrefix(got, "-") {
		t.Fatalf("atomic script = %q", got)
	}
	if got := c.do("EXISTS", "a"); got != ":0\r\n" {
		t.Fatalf("atomic script left a = %q", got)
	}

	ok := "#!lua flags=atomic\nredis.call('SET','b','2'); return redis.call('INCR','b')"
	if got := c.do(luaArgs(ok)...); got != ":3\r\n" {
		t.Fatalf("committing atomic script = %q", got)
	}
	if got := c.do("GET", "b"); got != "$1\r\n3\r\n" {
		t.Fatalf("b = %q", got)
	}
}

func TestAtomicFunctionRollsBackOnlyWhenFlagged(t *testing.T) {
	tcp := startAtomicTestServer(t)
	c := newAtomicTestClient(t, tcp)

	lib := "#!lua name=acid\n" +
		"local function body(keys, args) redis.call('SET', args[1], '1'); redis.call('LPUSH', args[1], 'x'); return 1 end\n" +
		"redis.register_function{function_name='plain', callback=body}\n" +
		"redis.register_function{function_name='safe', callback=body, flags={'atomic'}}\n"
	if got := c.do("FUNCTION", "LOAD", lib); strings.HasPrefix(got, "-") {
		t.Fatalf("FUNCTION LOAD = %q", got)
	}
	if got := c.do("FCALL", "plain", "0", "p"); !strings.HasPrefix(got, "-") {
		t.Fatalf("plain FCALL = %q", got)
	}
	if got := c.do("GET", "p"); got != "$1\r\n1\r\n" {
		t.Fatalf("plain function kept write = %q", got)
	}
	if got := c.do("FCALL", "safe", "0", "s"); !strings.HasPrefix(got, "-") {
		t.Fatalf("atomic FCALL = %q", got)
	}
	if got := c.do("EXISTS", "s"); got != ":0\r\n" {
		t.Fatalf("atomic function left s = %q", got)
	}
}

func TestAtomicTransactionsFlagMakesPlainMultiAndScriptsAtomic(t *testing.T) {
	cfg := config.Default()
	cfg.ListenAddr = "127.0.0.1:0"
	cfg.AtomicTransactions = true
	tcp, err := ListenWithConfig(cfg, engine.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tcp.Close() })
	c := newAtomicTestClient(t, tcp)

	c.do("MULTI")
	c.do("SET", "x", "1")
	c.do("LPUSH", "x", "y")
	if got := c.do("EXEC"); !strings.HasPrefix(got, "-EXECABORT Atomic transaction rolled back") {
		t.Fatalf("plain MULTI under the flag = %q", got)
	}
	if got := c.do("EXISTS", "x"); got != ":0\r\n" {
		t.Fatalf("x = %q", got)
	}

	if got := c.do(luaArgs("redis.call('SET','z','1'); redis.call('LPUSH','z','x'); return 1")...); !strings.HasPrefix(got, "-") {
		t.Fatalf("script = %q", got)
	}
	if got := c.do("EXISTS", "z"); got != ":0\r\n" {
		t.Fatalf("script under the flag left z = %q", got)
	}
}
