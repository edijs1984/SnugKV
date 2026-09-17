package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func loadFunctionLibrary(t *testing.T, s *Server, code string) string {
	t.Helper()
	response, err := s.Execute([][]byte{[]byte("FUNCTION"), []byte("LOAD"), []byte(code)})
	if err != nil {
		t.Fatalf("FUNCTION LOAD error: %v", err)
	}
	return string(response)
}

func TestFunctionLoadAndFCallKeysArgv(t *testing.T) {
	s := New(engine.New())
	code := "#!lua name=basic\nredis.register_function('echo', function(keys, args) return {keys[1], args[1]} end)"
	if got := loadFunctionLibrary(t, s, code); got != "$5\r\nbasic\r\n" {
		t.Fatalf("FUNCTION LOAD = %q", got)
	}
	if got := execute(t, s, "FCALL", "echo", "1", "mykey", "hello"); got != "*2\r\n$5\r\nmykey\r\n$5\r\nhello\r\n" {
		t.Fatalf("FCALL = %q", got)
	}
}

func TestFunctionWritableAndReadOnlyCalls(t *testing.T) {
	s := New(engine.New())
	code := "#!lua name=rwlib\n" +
		"redis.register_function('writer', function(keys,args) return redis.call('SET',keys[1],args[1]) end)\n" +
		"redis.register_function{function_name='reader',callback=function(keys,args) return redis.call('GET',keys[1]) end,flags={'no-writes'}}"
	loadFunctionLibrary(t, s, code)

	if got := execute(t, s, "FCALL", "writer", "1", "fn:key", "value"); got != "+OK\r\n" {
		t.Fatalf("FCALL writer = %q", got)
	}
	if got := execute(t, s, "FCALL", "reader", "1", "fn:key"); got != "$5\r\nvalue\r\n" {
		t.Fatalf("FCALL no-writes reader = %q", got)
	}

	args := [][]byte{[]byte("FCALL_RO"), []byte("writer"), []byte("1"), []byte("fn:key"), []byte("changed")}
	if _, err := s.Execute(args); err == nil || !strings.Contains(err.Error(), "Write commands are not allowed from read-only scripts.") {
		t.Fatalf("FCALL_RO writer error = %v", err)
	}
	if got := execute(t, s, "GET", "fn:key"); got != "$5\r\nvalue\r\n" {
		t.Fatalf("FCALL_RO changed key: %q", got)
	}
}

func TestFunctionNoWritesFlagAppliesToFCall(t *testing.T) {
	s := New(engine.New())
	code := "#!lua name=readonly\nredis.register_function{function_name='badwriter',callback=function(keys,args) return redis.call('SET',keys[1],'x') end,flags={'no-writes'}}"
	loadFunctionLibrary(t, s, code)
	args := [][]byte{[]byte("FCALL"), []byte("badwriter"), []byte("1"), []byte("fn:key")}
	if _, err := s.Execute(args); err == nil || !strings.Contains(err.Error(), "Write commands are not allowed from read-only scripts.") {
		t.Fatalf("no-writes FCALL error = %v", err)
	}
	if got := execute(t, s, "EXISTS", "fn:key"); got != ":0\r\n" {
		t.Fatalf("no-writes function mutated key: %q", got)
	}
}

func TestFunctionLocalStatePersistsAcrossCalls(t *testing.T) {
	s := New(engine.New())
	code := "#!lua name=stateful\nlocal n=0; redis.register_function('counter', function(keys,args) n=n+1; return n end)"
	loadFunctionLibrary(t, s, code)
	if got := execute(t, s, "FCALL", "counter", "0"); got != ":1\r\n" {
		t.Fatalf("first counter = %q", got)
	}
	if got := execute(t, s, "FCALL", "counter", "0"); got != ":2\r\n" {
		t.Fatalf("second counter = %q", got)
	}
}

func TestFunctionListWithCodeAndMetadata(t *testing.T) {
	s := New(engine.New())
	code := "#!lua name=listlib\nredis.register_function{function_name='reader',callback=function(keys,args) return 1 end,description='reads things',flags={'no-writes'}}"
	loadFunctionLibrary(t, s, code)
	got := execute(t, s, "FUNCTION", "LIST", "LIBRARYNAME", "list*", "WITHCODE")
	for _, expected := range []string{"library_name", "listlib", "engine", "LUA", "functions", "reader", "description", "reads things", "flags", "no-writes", "library_code", code} {
		if !strings.Contains(got, expected) {
			t.Fatalf("FUNCTION LIST missing %q in %q", expected, got)
		}
	}
	if got := execute(t, s, "FUNCTION", "LIST", "LIBRARYNAME", "other*"); got != "*0\r\n" {
		t.Fatalf("FUNCTION LIST filtered = %q", got)
	}
}

func TestFunctionReplaceDeleteAndFlush(t *testing.T) {
	s := New(engine.New())
	first := "#!lua name=repl\nredis.register_function('version', function(keys,args) return 'one' end)"
	second := "#!lua name=repl\nredis.register_function('version', function(keys,args) return 'two' end)"
	loadFunctionLibrary(t, s, first)
	if _, err := s.Execute([][]byte{[]byte("FUNCTION"), []byte("LOAD"), []byte(second)}); err == nil || !strings.Contains(err.Error(), "Library 'repl' already exists") {
		t.Fatalf("duplicate library error = %v", err)
	}
	if got := execute(t, s, "FUNCTION", "LOAD", "REPLACE", second); got != "$4\r\nrepl\r\n" {
		t.Fatalf("FUNCTION LOAD REPLACE = %q", got)
	}
	if got := execute(t, s, "FCALL", "version", "0"); got != "$3\r\ntwo\r\n" {
		t.Fatalf("replaced function = %q", got)
	}
	if got := execute(t, s, "FUNCTION", "DELETE", "repl"); got != "+OK\r\n" {
		t.Fatalf("FUNCTION DELETE = %q", got)
	}
	if _, err := s.Execute([][]byte{[]byte("FCALL"), []byte("version"), []byte("0")}); err == nil || err.Error() != "ERR Function not found" {
		t.Fatalf("FCALL after DELETE error = %v", err)
	}

	loadFunctionLibrary(t, s, "#!lua name=a\nredis.register_function('a_fn', function(keys,args) return 1 end)")
	loadFunctionLibrary(t, s, "#!lua name=b\nredis.register_function('b_fn', function(keys,args) return 1 end)")
	if got := execute(t, s, "FUNCTION", "FLUSH", "ASYNC"); got != "+OK\r\n" {
		t.Fatalf("FUNCTION FLUSH = %q", got)
	}
	if got := execute(t, s, "FUNCTION", "LIST"); got != "*0\r\n" {
		t.Fatalf("FUNCTION LIST after FLUSH = %q", got)
	}
}

func TestFunctionNameCollisionAndValidation(t *testing.T) {
	s := New(engine.New())
	loadFunctionLibrary(t, s, "#!lua name=one\nredis.register_function('Shared', function(keys,args) return 1 end)")
	if _, err := s.Execute([][]byte{[]byte("FUNCTION"), []byte("LOAD"), []byte("#!lua name=two\nredis.register_function('shared', function(keys,args) return 2 end)")}); err == nil || !strings.Contains(err.Error(), "Function shared already exists") {
		t.Fatalf("function collision error = %v", err)
	}
	if _, err := s.Execute([][]byte{[]byte("FUNCTION"), []byte("LOAD"), []byte("redis.register_function('x', function() return 1 end)")}); err == nil || !strings.Contains(err.Error(), "Missing library metadata") {
		t.Fatalf("missing metadata error = %v", err)
	}
	unsupported := "#!lua name=badflag\nredis.register_function{function_name='x',callback=function() return 1 end,flags={'allow-oom'}}"
	if _, err := s.Execute([][]byte{[]byte("FUNCTION"), []byte("LOAD"), []byte(unsupported)}); err == nil || !strings.Contains(err.Error(), "unsupported function flag") {
		t.Fatalf("unsupported flag error = %v", err)
	}
}

func TestFunctionRuntimeErrorKeepsEarlierWrites(t *testing.T) {
	s := New(engine.New())
	code := "#!lua name=partial\nredis.register_function('partial', function(keys,args) redis.call('SET',keys[1],'written'); error('boom') end)"
	loadFunctionLibrary(t, s, code)
	args := [][]byte{[]byte("FCALL"), []byte("partial"), []byte("1"), []byte("fn:partial")}
	if _, err := s.Execute(args); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("FCALL runtime error = %v", err)
	}
	if got := execute(t, s, "GET", "fn:partial"); got != "$7\r\nwritten\r\n" {
		t.Fatalf("write before function error was lost: %q", got)
	}
}
