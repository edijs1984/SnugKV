package server

import (
	"os"
	"path/filepath"
	"testing"

	"snugkv/internal/config"
	"snugkv/internal/engine"
)

func newFunctionPersistenceTestServer(t *testing.T) *TCPServer {
	t.Helper()
	cfg := config.Default()
	cfg.ListenAddr = "127.0.0.1:0"
	listener, err := ListenWithJournal(cfg, engine.New(), nil)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func TestFunctionLibrariesPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	snapshotPath := filepath.Join(dir, "snapshot.db")

	first := newFunctionPersistenceTestServer(t)
	if err := first.ConfigureFunctionPersistence("", snapshotPath); err != nil {
		t.Fatalf("configure first persistence: %v", err)
	}
	code := "#!lua name=persisted\nredis.register_function('hello', function(keys,args) return 'survived' end)"
	if _, err := first.server.Execute([][]byte{[]byte("FUNCTION"), []byte("LOAD"), []byte(code)}); err != nil {
		t.Fatalf("FUNCTION LOAD: %v", err)
	}
	statePath := snapshotPath + ".functions"
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("function state file: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first server: %v", err)
	}

	second := newFunctionPersistenceTestServer(t)
	if err := second.ConfigureFunctionPersistence("", snapshotPath); err != nil {
		t.Fatalf("configure second persistence: %v", err)
	}
	if got := execute(t, second.server, "FCALL", "hello", "0"); got != "$8\r\nsurvived\r\n" {
		t.Fatalf("FCALL after restart = %q", got)
	}
}

func TestFunctionFlushPersistsEmptyRegistry(t *testing.T) {
	dir := t.TempDir()
	aofPath := filepath.Join(dir, "appendonly.aof")

	first := newFunctionPersistenceTestServer(t)
	if err := first.ConfigureFunctionPersistence(aofPath, ""); err != nil {
		t.Fatalf("configure persistence: %v", err)
	}
	code := "#!lua name=gone\nredis.register_function('gone_fn', function() return 1 end)"
	if _, err := first.server.Execute([][]byte{[]byte("FUNCTION"), []byte("LOAD"), []byte(code)}); err != nil {
		t.Fatalf("FUNCTION LOAD: %v", err)
	}
	if got := execute(t, first.server, "FUNCTION", "FLUSH"); got != "+OK\r\n" {
		t.Fatalf("FUNCTION FLUSH = %q", got)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first server: %v", err)
	}

	second := newFunctionPersistenceTestServer(t)
	if err := second.ConfigureFunctionPersistence(aofPath, ""); err != nil {
		t.Fatalf("configure second persistence: %v", err)
	}
	if got := execute(t, second.server, "FUNCTION", "LIST"); got != "*0\r\n" {
		t.Fatalf("FUNCTION LIST after restart = %q", got)
	}
}

func TestFunctionRecoveryRejectsCorruptState(t *testing.T) {
	dir := t.TempDir()
	snapshotPath := filepath.Join(dir, "snapshot.db")
	statePath := snapshotPath + ".functions"
	if err := os.WriteFile(statePath, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	listener := newFunctionPersistenceTestServer(t)
	if err := listener.ConfigureFunctionPersistence("", snapshotPath); err == nil {
		t.Fatal("corrupt function state unexpectedly accepted")
	}
}
