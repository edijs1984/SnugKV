package server

import (
	"strings"
	"testing"
	"time"

	"snugkv/internal/engine"
)

func TestFunctionStatsIdleAndCounts(t *testing.T) {
	s := New(engine.New())
	wantEmpty := "*4\r\n$14\r\nrunning_script\r\n$-1\r\n$7\r\nengines\r\n*2\r\n$3\r\nLUA\r\n*4\r\n$15\r\nlibraries_count\r\n:0\r\n$15\r\nfunctions_count\r\n:0\r\n"
	if got := execute(t, s, "FUNCTION", "STATS"); got != wantEmpty {
		t.Fatalf("empty FUNCTION STATS = %q", got)
	}

	loadFunctionLibrary(t, s, "#!lua name=stats_a\nredis.register_function('one', function(keys,args) return 1 end)")
	loadFunctionLibrary(t, s, "#!lua name=stats_b\nredis.register_function('two', function(keys,args) return 2 end); redis.register_function('three', function(keys,args) return 3 end)")
	got := execute(t, s, "FUNCTION", "STATS")
	for _, expected := range []string{"running_script", "engines", "LUA", "libraries_count\r\n:2", "functions_count\r\n:3"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("FUNCTION STATS missing %q in %q", expected, got)
		}
	}
}

func TestFunctionStatsReportsRunningFunction(t *testing.T) {
	s := New(engine.New())
	loadFunctionLibrary(t, s, "#!lua name=statslive\nredis.register_function('work', function(keys,args) return 1 end)")
	finish := beginRunningFunction(s, [][]byte{[]byte("FCALL"), []byte("work"), []byte("1"), []byte("key"), []byte("arg")})
	defer finish()

	got := execute(t, s, "FUNCTION", "STATS")
	for _, expected := range []string{"running_script", "name", "work", "command", "FCALL", "key", "arg", "duration_ms", "libraries_count\r\n:1", "functions_count\r\n:1"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("live FUNCTION STATS missing %q in %q", expected, got)
		}
	}
}

func TestFunctionStatsBypassesDurabilityMutex(t *testing.T) {
	s := New(engine.New())
	s.durableMu.Lock()
	defer s.durableMu.Unlock()

	done := make(chan error, 1)
	go func() {
		_, err := s.Execute([][]byte{[]byte("FUNCTION"), []byte("STATS")})
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("FUNCTION STATS error while durableMu held: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("FUNCTION STATS blocked on durableMu")
	}
}

func TestFunctionHelp(t *testing.T) {
	s := New(engine.New())
	got := execute(t, s, "FUNCTION", "HELP")
	for _, expected := range []string{
		"FUNCTION <subcommand>",
		"LOAD [REPLACE] <FUNCTION CODE>",
		"LIST [LIBRARYNAME PATTERN] [WITHCODE]",
		"STATS",
		"KILL",
		"DUMP",
		"RESTORE <PAYLOAD> [FLUSH|APPEND|REPLACE]",
		"HELP",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("FUNCTION HELP missing %q in %q", expected, got)
		}
	}
}

func TestFunctionStatsHelpArity(t *testing.T) {
	s := New(engine.New())
	if _, err := s.Execute([][]byte{[]byte("FUNCTION"), []byte("STATS"), []byte("extra")}); err == nil || err.Error() != "ERR wrong number of arguments for 'function|stats' command" {
		t.Fatalf("FUNCTION STATS arity error = %v", err)
	}
	if _, err := s.Execute([][]byte{[]byte("FUNCTION"), []byte("HELP"), []byte("extra")}); err == nil || err.Error() != "ERR wrong number of arguments for 'function|help' command" {
		t.Fatalf("FUNCTION HELP arity error = %v", err)
	}
}
