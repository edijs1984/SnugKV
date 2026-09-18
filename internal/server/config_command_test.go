package server

import (
	"bytes"
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func newConfigTestServer(
	t *testing.T,
) *Server {
	t.Helper()

	store, err := engine.NewWithOptions(
		engine.Options{
			Shards: 4,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	s := New(store)
	s.eviction = "noeviction"
	s.configAppendFsync = "everysec"

	maxClients := 10000

	s.configGetMaxClients = func() int {
		return maxClients
	}

	s.configSetMaxClients = func(max int) {
		maxClients = max
	}

	return s
}

func TestConfigGetExact(t *testing.T) {
	s := newConfigTestServer(t)

	reply, err := s.executeConfig(
		[][]byte{
			[]byte("CONFIG"),
			[]byte("GET"),
			[]byte("maxmemory"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	want := array(
		formatBulkString(
			[]byte("maxmemory"),
		),
		formatBulkString(
			[]byte("0"),
		),
	)

	if !bytes.Equal(reply, want) {
		t.Fatalf(
			"unexpected reply:\n%q\nwant:\n%q",
			reply,
			want,
		)
	}
}

func TestConfigGetPattern(t *testing.T) {
	s := newConfigTestServer(t)

	reply, err := s.executeConfig(
		[][]byte{
			[]byte("CONFIG"),
			[]byte("GET"),
			[]byte("max*"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	text := string(reply)

	for _, expected := range []string{
		"maxmemory",
		"maxmemory-policy",
		"maxclients",
	} {
		if !strings.Contains(
			text,
			expected,
		) {
			t.Fatalf(
				"missing %q in %q",
				expected,
				text,
			)
		}
	}
}

func TestConfigGetMultipleDeduplicates(t *testing.T) {
	s := newConfigTestServer(t)

	reply, err := s.executeConfig(
		[][]byte{
			[]byte("CONFIG"),
			[]byte("GET"),
			[]byte("maxmemory"),
			[]byte("max*"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Count(
		reply,
		[]byte("maxmemory\r\n"),
	) != 1 {
		t.Fatalf(
			"maxmemory duplicated: %q",
			reply,
		)
	}
}

func TestConfigSetMaxMemory(t *testing.T) {
	s := newConfigTestServer(t)

	if err := s.configSet(
		"maxmemory",
		"64mb",
	); err != nil {
		t.Fatal(err)
	}

	if got := s.store.MaxMemory(); got != 64<<20 {
		t.Fatalf(
			"maxmemory=%d want=%d",
			got,
			uint64(64<<20),
		)
	}
}

func TestConfigSetMaxMemoryInvalid(t *testing.T) {
	s := newConfigTestServer(t)

	err := s.configSet(
		"maxmemory",
		"-1",
	)
	if err == nil ||
		!strings.Contains(
			err.Error(),
			"argument must be a memory value",
		) {

		t.Fatalf(
			"unexpected error: %v",
			err,
		)
	}
}

func TestConfigSetEvictionPolicy(t *testing.T) {
	s := newConfigTestServer(t)

	if err := s.configSet(
		"maxmemory-policy",
		"allkeys-lru",
	); err != nil {
		t.Fatal(err)
	}

	if s.eviction != "allkeys-lru" {
		t.Fatalf(
			"eviction=%q",
			s.eviction,
		)
	}
}

func TestConfigRejectsUnsupportedEvictionPolicy(
	t *testing.T,
) {
	s := newConfigTestServer(t)

	err := s.configSet(
		"maxmemory-policy",
		"allkeys-lfu",
	)
	if err == nil {
		t.Fatal(
			"expected unsupported policy error",
		)
	}
}

func TestConfigSetMaxClients(t *testing.T) {
	s := newConfigTestServer(t)

	if err := s.configSet(
		"maxclients",
		"1234",
	); err != nil {
		t.Fatal(err)
	}

	reply := s.configGet(
		[][]byte{
			[]byte("maxclients"),
		},
	)

	if !bytes.Contains(
		reply,
		[]byte("1234"),
	) {
		t.Fatalf(
			"unexpected reply: %q",
			reply,
		)
	}
}

func TestConfigRewriteWithoutConfigFile(
	t *testing.T,
) {
	s := newConfigTestServer(t)

	_, err := s.executeConfig(
		[][]byte{
			[]byte("CONFIG"),
			[]byte("REWRITE"),
		},
	)

	if err == nil ||
		err.Error() !=
			"ERR The server is running without a config file" {

		t.Fatalf(
			"unexpected error: %v",
			err,
		)
	}
}

func TestConfigUnknownSubcommand(t *testing.T) {
	s := newConfigTestServer(t)

	_, err := s.executeConfig(
		[][]byte{
			[]byte("CONFIG"),
			[]byte("WHATEVER"),
		},
	)

	if err == nil ||
		err.Error() !=
			"ERR unknown subcommand 'WHATEVER'. Try CONFIG HELP." {

		t.Fatalf(
			"unexpected error: %v",
			err,
		)
	}
}

func TestConfigHelp(t *testing.T) {
	s := newConfigTestServer(t)

	reply, err := s.executeConfig(
		[][]byte{
			[]byte("CONFIG"),
			[]byte("HELP"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, part := range []string{
		"CONFIG <subcommand>",
		"GET <pattern>",
		"RESETSTAT",
		"REWRITE",
	} {
		if !bytes.Contains(
			reply,
			[]byte(part),
		) {
			t.Fatalf(
				"help missing %q",
				part,
			)
		}
	}
}

func TestConfigResetStat(t *testing.T) {
	s := newConfigTestServer(t)

	s.commands = 42

	s.metrics.Observe(
		"GET",
		1,
		false,
	)

	reply, err := s.executeConfig(
		[][]byte{
			[]byte("CONFIG"),
			[]byte("RESETSTAT"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if string(reply) != "+OK\r\n" {
		t.Fatalf(
			"unexpected reply: %q",
			reply,
		)
	}

	if s.commands != 0 {
		t.Fatalf(
			"commands=%d want=0",
			s.commands,
		)
	}
}

func TestConfigSetMultiplePairs(t *testing.T) {
	s := newConfigTestServer(t)

	reply, err := s.executeConfig(
		[][]byte{
			[]byte("CONFIG"),
			[]byte("SET"),
			[]byte("maxmemory"),
			[]byte("32mb"),
			[]byte("maxmemory-policy"),
			[]byte("allkeys-lru"),
			[]byte("maxclients"),
			[]byte("1234"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if string(reply) != "+OK\r\n" {
		t.Fatalf(
			"unexpected reply: %q",
			reply,
		)
	}

	if got := s.store.MaxMemory(); got != 32<<20 {
		t.Fatalf(
			"maxmemory=%d want=%d",
			got,
			uint64(32<<20),
		)
	}

	if s.eviction != "allkeys-lru" {
		t.Fatalf(
			"eviction=%q",
			s.eviction,
		)
	}

	maxClientsReply := s.configGet(
		[][]byte{
			[]byte("maxclients"),
		},
	)

	if !bytes.Contains(
		maxClientsReply,
		[]byte("1234"),
	) {
		t.Fatalf(
			"maxclients not updated: %q",
			maxClientsReply,
		)
	}
}

func TestConfigSetMultiplePairsValidatesBeforeApply(
	t *testing.T,
) {
	s := newConfigTestServer(t)

	beforeMemory := s.store.MaxMemory()

	_, err := s.executeConfig(
		[][]byte{
			[]byte("CONFIG"),
			[]byte("SET"),
			[]byte("maxmemory"),
			[]byte("32mb"),
			[]byte("does-not-exist"),
			[]byte("x"),
		},
	)
	if err == nil {
		t.Fatal(
			"expected CONFIG SET validation error",
		)
	}

	if got := s.store.MaxMemory(); got != beforeMemory {
		t.Fatalf(
			"partial CONFIG SET applied: maxmemory=%d before=%d",
			got,
			beforeMemory,
		)
	}
}

func TestConfigCommandInfoParent(t *testing.T) {
	reply := commandInfoReply("CONFIG")

	for _, expected := range []string{
		"config",
		"config|resetstat",
		"config|get",
		"config|set",
		"config|help",
		"config|rewrite",
	} {
		if !bytes.Contains(
			reply,
			[]byte(expected),
		) {
			t.Fatalf(
				"CONFIG metadata missing %q: %q",
				expected,
				reply,
			)
		}
	}
}

func TestConfigCommandInfoGet(t *testing.T) {
	reply := commandInfoReply(
		"CONFIG|GET",
	)

	for _, expected := range []string{
		"config|get",
		"admin",
		"noscript",
		"loading",
		"stale",
		"@admin",
		"@slow",
		"@dangerous",
	} {
		if !bytes.Contains(
			reply,
			[]byte(expected),
		) {
			t.Fatalf(
				"CONFIG|GET metadata missing %q: %q",
				expected,
				reply,
			)
		}
	}
}

func TestConfigCommandInfoSetTips(t *testing.T) {
	reply := commandInfoReply(
		"CONFIG|SET",
	)

	for _, expected := range []string{
		"request_policy:all_nodes",
		"response_policy:all_succeeded",
	} {
		if !bytes.Contains(
			reply,
			[]byte(expected),
		) {
			t.Fatalf(
				"CONFIG|SET metadata missing %q: %q",
				expected,
				reply,
			)
		}
	}
}

func TestConfigCommandDocsParent(t *testing.T) {
	reply := commandDocsReply(
		[][]byte{
			[]byte("CONFIG"),
		},
	)

	for _, expected := range []string{
		"A container for server configuration commands.",
		"config|resetstat",
		"config|get",
		"config|set",
		"config|help",
		"config|rewrite",
	} {
		if !bytes.Contains(
			reply,
			[]byte(expected),
		) {
			t.Fatalf(
				"CONFIG DOCS missing %q: %q",
				expected,
				reply,
			)
		}
	}
}

func TestConfigCommandDocsGetHistory(t *testing.T) {
	reply := commandDocsReply(
		[][]byte{
			[]byte("CONFIG|GET"),
		},
	)

	for _, expected := range []string{
		"Returns the effective values of configuration parameters.",
		"7.0.0",
		"multiple",
	} {
		if !bytes.Contains(
			reply,
			[]byte(expected),
		) {
			t.Fatalf(
				"CONFIG|GET DOCS missing %q: %q",
				expected,
				reply,
			)
		}
	}
}
