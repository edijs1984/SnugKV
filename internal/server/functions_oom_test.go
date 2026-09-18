package server

import (
	"errors"
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func loadOOMFunctionLibrary(t *testing.T, s *Server) {
	t.Helper()

	code := "#!lua name=oomtest\n" +
		"redis.register_function('plain_read', function(keys,args) return redis.call('GET',keys[1]) end)\n" +
		"redis.register_function('plain_write', function(keys,args) return redis.call('SET',keys[1],args[1]) end)\n" +
		"redis.register_function{function_name='allow_read',callback=function(keys,args) return redis.call('GET',keys[1]) end,flags={'allow-oom'}}\n" +
		"redis.register_function{function_name='allow_write',callback=function(keys,args) return redis.call('SET',keys[1],args[1]) end,flags={'allow-oom'}}\n" +
		"redis.register_function{function_name='readonly',callback=function(keys,args) return redis.call('GET',keys[1]) end,flags={'no-writes'}}\n" +
		"redis.register_function{function_name='readonly_badwrite',callback=function(keys,args) return redis.call('SET',keys[1],args[1]) end,flags={'no-writes'}}"

	loadFunctionLibrary(t, s, code)
}

func forceFunctionOOMState(t *testing.T, s *Server) {
	t.Helper()

	if got := execute(t, s, "SET", "oom:seed", strings.Repeat("x", 4096)); got != "+OK\r\n" {
		t.Fatalf("seed SET = %q", got)
	}

	used := s.store.Memory().AccountedBytes
	if used == 0 {
		t.Fatal("expected non-zero accounted memory")
	}

	// Redis permits lowering maxmemory below current usage. That creates the
	// exact pre-existing OOM state used by the live differential audit.
	s.store.SetMaxMemory(1)
}

func TestFunctionPlainFCallRejectedWhenAlreadyOOM(t *testing.T) {
	s := New(engine.New())
	loadOOMFunctionLibrary(t, s)
	forceFunctionOOMState(t, s)

	for _, args := range [][][]byte{
		{[]byte("FCALL"), []byte("plain_read"), []byte("1"), []byte("oom:seed")},
		{[]byte("FCALL"), []byte("plain_write"), []byte("1"), []byte("oom:plain"), []byte("value")},
		{[]byte("FCALL_RO"), []byte("plain_read"), []byte("1"), []byte("oom:seed")},
	} {
		if _, err := s.Execute(args); !errors.Is(err, engine.ErrOOM) {
			t.Fatalf("%q error = %v, want engine.ErrOOM", args, err)
		}
	}
}

func TestFunctionAllowOOMMayReadAndGrowPastLimit(t *testing.T) {
	s := New(engine.New())
	loadOOMFunctionLibrary(t, s)
	forceFunctionOOMState(t, s)

	if got := execute(t, s, "FCALL", "allow_read", "1", "oom:seed"); !strings.Contains(got, strings.Repeat("x", 128)) {
		t.Fatalf("allow-oom read = %q", got)
	}

	before := s.store.Memory().AccountedBytes

	if got := execute(t, s, "FCALL", "allow_write", "1", "oom:allow", "value"); got != "+OK\r\n" {
		t.Fatalf("allow-oom write = %q", got)
	}

	after := s.store.Memory().AccountedBytes
	if after <= before {
		t.Fatalf("allow-oom write did not grow memory: before=%d after=%d", before, after)
	}

	if got := execute(t, s, "GET", "oom:allow"); got != "$5\r\nvalue\r\n" {
		t.Fatalf("allow-oom written value = %q", got)
	}
}

func TestFunctionNoWritesMayRunWhileOOMButCannotWrite(t *testing.T) {
	s := New(engine.New())
	loadOOMFunctionLibrary(t, s)
	forceFunctionOOMState(t, s)

	if got := execute(t, s, "FCALL", "readonly", "1", "oom:seed"); !strings.Contains(got, strings.Repeat("x", 128)) {
		t.Fatalf("no-writes read = %q", got)
	}

	args := [][]byte{
		[]byte("FCALL"),
		[]byte("readonly_badwrite"),
		[]byte("1"),
		[]byte("oom:ro"),
		[]byte("value"),
	}
	if _, err := s.Execute(args); err == nil ||
		!strings.Contains(err.Error(), "Write commands are not allowed from read-only scripts.") {
		t.Fatalf("no-writes attempted write error = %v", err)
	}
}

func TestFunctionAllowOOMBypassEndsAfterInvocation(t *testing.T) {
	s := New(engine.New())
	loadOOMFunctionLibrary(t, s)
	forceFunctionOOMState(t, s)

	if got := execute(t, s, "FCALL", "allow_write", "1", "oom:allow", "value"); got != "+OK\r\n" {
		t.Fatalf("allow-oom write = %q", got)
	}

	if s.store.MaxMemory() != 1 {
		t.Fatalf("maxmemory after FCALL = %d, want 1", s.store.MaxMemory())
	}

	_, err := s.Execute([][]byte{
		[]byte("SET"),
		[]byte("oom:after"),
		[]byte("value"),
	})
	if !errors.Is(err, engine.ErrOOM) {
		t.Fatalf("SET after allow-oom FCALL error = %v, want engine.ErrOOM", err)
	}
}

func TestFunctionAllowOOMFlagListed(t *testing.T) {
	s := New(engine.New())

	code := "#!lua name=oomflag\n" +
		"redis.register_function{function_name='writer',callback=function(keys,args) return 1 end,flags={'allow-oom'}}"

	loadFunctionLibrary(t, s, code)

	got := execute(t, s, "FUNCTION", "LIST", "LIBRARYNAME", "oomflag")
	if !strings.Contains(got, "allow-oom") {
		t.Fatalf("FUNCTION LIST missing allow-oom flag: %q", got)
	}
}
