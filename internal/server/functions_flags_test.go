package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestFunctionRegistrationSupportsStandaloneFlags(t *testing.T) {
	s := New(engine.New())

	code := "#!lua name=flagslib\n" +
		"redis.register_function{" +
		"function_name='flagged'," +
		"callback=function(keys,args) return 'ok' end," +
		"flags={'no-writes','allow-stale','no-cluster','allow-cross-slot-keys'}" +
		"}"

	if got := execute(t, s, "FUNCTION", "LOAD", code); got != "$8\r\nflagslib\r\n" {
		t.Fatalf("FUNCTION LOAD = %q", got)
	}

	fn := functionRegistryForServer(s).lookup("flagged")
	if fn == nil {
		t.Fatal("registered function not found")
	}
	if !fn.noWrites {
		t.Fatal("no-writes flag not recorded")
	}
	if !fn.allowStale {
		t.Fatal("allow-stale flag not recorded")
	}
	if !fn.noCluster {
		t.Fatal("no-cluster flag not recorded")
	}
	if !fn.allowCrossSlotKeys {
		t.Fatal("allow-cross-slot-keys flag not recorded")
	}

	got := execute(t, s, "FUNCTION", "LIST")
	for _, want := range []string{
		"+no-writes\r\n",
		"+allow-stale\r\n",
		"+no-cluster\r\n",
		"+allow-cross-slot-keys\r\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("FUNCTION LIST missing %q in %q", want, got)
		}
	}
}

func TestFunctionFlagsDeduplicate(t *testing.T) {
	s := New(engine.New())

	code := "#!lua name=dupeflags\n" +
		"redis.register_function{" +
		"function_name='f'," +
		"callback=function(keys,args) return 1 end," +
		"flags={'no-writes','no-writes','allow-stale','allow-stale'}" +
		"}"

	if got := execute(t, s, "FUNCTION", "LOAD", code); got != "$9\r\ndupeflags\r\n" {
		t.Fatalf("FUNCTION LOAD = %q", got)
	}

	fn := functionRegistryForServer(s).lookup("f")
	if fn == nil {
		t.Fatal("registered function not found")
	}

	if len(fn.flags) != 2 {
		t.Fatalf("flags = %#v, want 2 unique flags", fn.flags)
	}
	if fn.flags[0] != "no-writes" || fn.flags[1] != "allow-stale" {
		t.Fatalf("flags = %#v", fn.flags)
	}
}

func TestFunctionAllowOOMFlagSupported(t *testing.T) {
	s := New(engine.New())

	code := "#!lua name=oomflag\n" +
		"redis.register_function{" +
		"function_name='f'," +
		"callback=function(keys,args) return 1 end," +
		"flags={'allow-oom'}" +
		"}"

	if got := execute(t, s, "FUNCTION", "LOAD", code); got != "$7\r\noomflag\r\n" {
		t.Fatalf("FUNCTION LOAD = %q", got)
	}

	fn := functionRegistryForServer(s).lookup("f")
	if fn == nil {
		t.Fatal("registered function not found")
	}
	if !fn.allowOom {
		t.Fatal("allow-oom flag not recorded")
	}

	got := execute(t, s, "FUNCTION", "LIST")
	if !strings.Contains(got, "+allow-oom\r\n") {
		t.Fatalf("FUNCTION LIST missing allow-oom in %q", got)
	}
}

func TestFunctionUnknownFlagRejected(t *testing.T) {
	s := New(engine.New())

	code := "#!lua name=badflag\n" +
		"redis.register_function{" +
		"function_name='f'," +
		"callback=function(keys,args) return 1 end," +
		"flags={'definitely-not-a-redis-flag'}" +
		"}"

	_, err := s.Execute([][]byte{
		[]byte("FUNCTION"),
		[]byte("LOAD"),
		[]byte(code),
	})
	if err == nil {
		t.Fatal("FUNCTION LOAD unexpectedly accepted unknown flag")
	}
	if !strings.Contains(err.Error(), "unsupported function flag") {
		t.Fatalf("unexpected error: %v", err)
	}
}