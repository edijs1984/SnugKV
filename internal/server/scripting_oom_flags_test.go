package server

import (
	"errors"
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func newLuaOOMTestServer(t *testing.T) *Server {
	t.Helper()
	s := New(engine.New())
	s.eviction = "noeviction"

	for _, command := range [][]string{
		{"SET", "oom:read", "value"},
		{"SET", "oom:delete", "value"},
		{"SET", "oom:existing", "old"},
	} {
		if _, err := s.Execute(stringArgs(command...)); err != nil {
			t.Fatalf("seed %v: %v", command, err)
		}
	}
	s.store.SetMaxMemory(1)
	return s
}

func stringArgs(values ...string) [][]byte {
	out := make([][]byte, len(values))
	for i, value := range values {
		out[i] = []byte(value)
	}
	return out
}

func TestEvalScriptMetadataFlags(t *testing.T) {
	meta, err := parseEvalScriptMetadata(
		"#!lua flags=no-writes,allow-oom,allow-stale,no-cluster,allow-cross-slot-keys\nreturn 1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.flagged || !meta.noWrites || !meta.allowOom ||
		!meta.allowStale || !meta.noCluster || !meta.allowCrossSlotKeys {
		t.Fatalf("unexpected metadata: %#v", meta)
	}
	if meta.body != "return 1" {
		t.Fatalf("body = %q", meta.body)
	}
}

func TestEvalScriptMetadataRejectsUnknownFlag(t *testing.T) {
	_, err := parseEvalScriptMetadata(
		"#!lua flags=definitely-not-a-real-flag\nreturn 1",
	)
	if err == nil || err.Error() !=
		"ERR Unexpected flag in script shebang: definitely-not-a-real-flag" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLegacyEvalGrowingFirstWriteRejectedWhileOOM(t *testing.T) {
	s := newLuaOOMTestServer(t)

	_, err := s.Execute(stringArgs(
		"EVAL",
		"return redis.call('SET','oom:new','x')",
		"0",
	))
	if !errors.Is(err, engine.ErrOOM) &&
		(err == nil || !strings.Contains(err.Error(), "OOM command not allowed")) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLegacyEvalShrinkingWriteAdmitsLaterGrowingWrite(t *testing.T) {
	s := newLuaOOMTestServer(t)

	reply, err := s.Execute(stringArgs(
		"EVAL",
		"redis.call('DEL','oom:delete'); return redis.call('SET','oom:new','x')",
		"0",
	))
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != "+OK\r\n" {
		t.Fatalf("reply = %q", reply)
	}
	value, ok := s.store.Get("oom:new")
	if !ok || string(value) != "x" {
		t.Fatalf("oom:new = %q ok=%v", value, ok)
	}
}

func TestFlaggedEvalDefaultRejectedAtInvocationWhileOOM(t *testing.T) {
	s := newLuaOOMTestServer(t)

	_, err := s.Execute(stringArgs(
		"EVAL",
		"#!lua\nreturn redis.call('GET','oom:read')",
		"0",
	))
	if !errors.Is(err, engine.ErrOOM) {
		t.Fatalf("error = %v, want ErrOOM", err)
	}
}

func TestFlaggedEvalAllowOOMMayGrowAndRestoresLimit(t *testing.T) {
	s := newLuaOOMTestServer(t)

	reply, err := s.Execute(stringArgs(
		"EVAL",
		"#!lua flags=allow-oom\nreturn redis.call('SET','oom:new','x')",
		"0",
	))
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != "+OK\r\n" {
		t.Fatalf("reply = %q", reply)
	}
	if got := s.store.MaxMemory(); got != 1 {
		t.Fatalf("maxmemory after EVAL = %d, want 1", got)
	}
	value, ok := s.store.Get("oom:new")
	if !ok || string(value) != "x" {
		t.Fatalf("oom:new = %q ok=%v", value, ok)
	}
}

func TestFlaggedEvalNoWritesRunsWhileOOMAndRejectsWrite(t *testing.T) {
	s := newLuaOOMTestServer(t)

	reply, err := s.Execute(stringArgs(
		"EVAL",
		"#!lua flags=no-writes\nreturn redis.call('GET','oom:read')",
		"0",
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reply), "value") {
		t.Fatalf("reply = %q", reply)
	}

	_, err = s.Execute(stringArgs(
		"EVAL",
		"#!lua flags=no-writes\nreturn redis.call('SET','oom:new','x')",
		"0",
	))
	if err == nil || !strings.Contains(
		err.Error(),
		"Write commands are not allowed from read-only scripts.",
	) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEvalROFlaggedAdmissionOrdering(t *testing.T) {
	t.Run("default shebang OOM before write-flag rejection", func(t *testing.T) {
		s := newLuaOOMTestServer(t)
		_, err := s.Execute(stringArgs(
			"EVAL_RO",
			"#!lua\nreturn redis.call('GET','oom:read')",
			"0",
		))
		if !errors.Is(err, engine.ErrOOM) {
			t.Fatalf("error = %v, want ErrOOM", err)
		}
	})

	t.Run("allow-oom reaches write-flag rejection", func(t *testing.T) {
		s := newLuaOOMTestServer(t)
		_, err := s.Execute(stringArgs(
			"EVAL_RO",
			"#!lua flags=allow-oom\nreturn redis.call('SET','oom:new','x')",
			"0",
		))
		if err == nil || err.Error() !=
			"ERR Can not execute a script with write flag using *_ro command." {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("no-writes is allowed", func(t *testing.T) {
		s := newLuaOOMTestServer(t)
		reply, err := s.Execute(stringArgs(
			"EVAL_RO",
			"#!lua flags=no-writes\nreturn redis.call('GET','oom:read')",
			"0",
		))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(reply), "value") {
			t.Fatalf("reply = %q", reply)
		}
	})
}

func TestEvalSHAAllowOOMUsesCachedShebangMetadata(t *testing.T) {
	s := newLuaOOMTestServer(t)
	source := "#!lua flags=allow-oom\nreturn redis.call('SET','oom:sha','x')"

	// SCRIPT LOAD must be possible while the server is already OOM because it
	// only populates the script cache.
	reply, err := s.Execute(stringArgs("SCRIPT", "LOAD", source))
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSuffix(strings.TrimPrefix(string(reply), "$40\r\n"), "\r\n")
	if len(sha) != 40 {
		t.Fatalf("unexpected SCRIPT LOAD reply: %q", reply)
	}

	result, err := s.Execute(stringArgs("EVALSHA", sha, "0"))
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != "+OK\r\n" {
		t.Fatalf("EVALSHA = %q", result)
	}
	value, ok := s.store.Get("oom:sha")
	if !ok || string(value) != "x" {
		t.Fatalf("oom:sha = %q ok=%v", value, ok)
	}
}
