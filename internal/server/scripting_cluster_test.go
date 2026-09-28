package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func newClusterScriptingServer(t *testing.T, ranges map[string]string) *Server {
	t.Helper()
	s := New(engine.New())
	if err := s.configureClusterSlots(true, "127.0.0.1:7000", ranges); err != nil {
		t.Fatal(err)
	}
	return s
}


func TestClusterLuaScopeGuardDirect(t *testing.T) {
	s := newClusterScriptingServer(t, map[string]string{
		"0-8191":     "127.0.0.1:7000",
		"8192-16383": "127.0.0.1:7001",
	})

	scope := s.newLuaClusterScope(false)
	if scope == nil {
		t.Fatal("cluster scope is nil")
	}

	local := []byte("hello")
	remote := []byte("foo")
	if clusterKeySlot(local) >= 8192 {
		t.Fatalf("hello slot=%d, expected local range", clusterKeySlot(local))
	}
	if clusterKeySlot(remote) < 8192 {
		t.Fatalf("foo slot=%d, expected remote range", clusterKeySlot(remote))
	}

	if err := s.validateLuaClusterAccess(scope, [][]byte{[]byte("GET"), local}); err != nil {
		t.Fatalf("local access err=%v", err)
	}
	if err := s.validateLuaClusterAccess(scope, [][]byte{[]byte("GET"), remote}); err == nil ||
		!strings.Contains(err.Error(), "non local key") {
		t.Fatalf("remote access err=%v", err)
	}

	allLocal := newClusterScriptingServer(t, map[string]string{
		"0-16383": "127.0.0.1:7000",
	})
	scope = allLocal.newLuaClusterScope(false)
	if err := allLocal.validateLuaClusterAccess(scope, [][]byte{[]byte("GET"), []byte("hello")}); err != nil {
		t.Fatalf("first slot err=%v", err)
	}
	if err := allLocal.validateLuaClusterAccess(scope, [][]byte{[]byte("GET"), []byte("foo")}); err == nil ||
		!strings.Contains(err.Error(), "non local key") {
		t.Fatalf("cross-slot access err=%v", err)
	}
}

func TestClusterEvalDeclaredKeysRouteBeforeExecution(t *testing.T) {
	s := newClusterScriptingServer(t, map[string]string{
		"0-16383": "127.0.0.1:7000",
	})
	if clusterKeySlot([]byte("hello")) == clusterKeySlot([]byte("foo")) {
		t.Fatal("fixtures unexpectedly share a slot")
	}

	_, err := s.Execute([][]byte{
		[]byte("EVAL"), []byte("return 1"), []byte("2"),
		[]byte("hello"), []byte("foo"),
	})
	if err == nil || err.Error() != "CROSSSLOT Keys in request don't hash to the same slot" {
		t.Fatalf("cross-slot EVAL err=%v", err)
	}

	s = newClusterScriptingServer(t, map[string]string{
		"0-8191":     "127.0.0.1:7000",
		"8192-16383": "127.0.0.1:7001",
	})
	slot := clusterKeySlot([]byte("foo"))
	if slot < 8192 {
		t.Fatalf("foo slot=%d, expected remote range", slot)
	}
	_, err = s.Execute([][]byte{
		[]byte("EVAL"), []byte("return redis.call('GET',KEYS[1])"), []byte("1"), []byte("foo"),
	})
	want := "MOVED "
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("remote EVAL err=%v", err)
	}
}

func TestClusterEvalRuntimeLocalityAndCrossSlotFlag(t *testing.T) {
	s := newClusterScriptingServer(t, map[string]string{
		"0-16383": "127.0.0.1:7000",
	})
	if got := execute(t, s, "SET", "hello", "A"); got != "+OK\r\n" {
		t.Fatalf("SET hello=%q", got)
	}
	if got := execute(t, s, "SET", "foo", "B"); got != "+OK\r\n" {
		t.Fatalf("SET foo=%q", got)
	}

	legacy := "return {redis.call('GET','hello'),redis.call('GET','foo')}"
	if got := execute(t, s, "EVAL", legacy, "0"); got != "*2\r\n$1\r\nA\r\n$1\r\nB\r\n" {
		t.Fatalf("legacy cross-slot script=%q", got)
	}

	flagged := "#!lua\nreturn {redis.call('GET','hello'),redis.call('GET','foo')}"
	_, err := s.Execute([][]byte{[]byte("EVAL"), []byte(flagged), []byte("0")})
	if err == nil || !strings.Contains(err.Error(), "Script attempted to access a non local key in a cluster node") {
		t.Fatalf("flagged cross-slot EVAL err=%v", err)
	}

	allowed := "#!lua flags=allow-cross-slot-keys\nreturn {redis.call('GET','hello'),redis.call('GET','foo')}"
	if got := execute(t, s, "EVAL", allowed, "0"); got != "*2\r\n$1\r\nA\r\n$1\r\nB\r\n" {
		t.Fatalf("allow-cross-slot EVAL=%q", got)
	}
}

func TestClusterEvalRejectsNestedRemoteKeyAndNoCluster(t *testing.T) {
	s := newClusterScriptingServer(t, map[string]string{
		"0-8191":     "127.0.0.1:7000",
		"8192-16383": "127.0.0.1:7001",
	})

	legacy := "return redis.call('GET','foo')"
	_, err := s.Execute([][]byte{[]byte("EVAL"), []byte(legacy), []byte("0")})
	if err == nil || !strings.Contains(err.Error(), "Script attempted to access a non local key in a cluster node") {
		t.Fatalf("remote nested key err=%v", err)
	}

	noCluster := "#!lua flags=no-cluster\nreturn 1"
	_, err = s.Execute([][]byte{[]byte("EVAL"), []byte(noCluster), []byte("0")})
	if err == nil || !strings.Contains(err.Error(), "no-cluster") {
		t.Fatalf("no-cluster EVAL err=%v", err)
	}
}

func TestClusterFunctionRuntimeLocalityFlags(t *testing.T) {
	s := newClusterScriptingServer(t, map[string]string{
		"0-16383": "127.0.0.1:7000",
	})
	execute(t, s, "SET", "hello", "A")
	execute(t, s, "SET", "foo", "B")

	code := "#!lua name=clusterfn\n" +
		"redis.register_function('blocked', function(keys,args) return {redis.call('GET','hello'),redis.call('GET','foo')} end)\n" +
		"redis.register_function{function_name='allowed',callback=function(keys,args) return {redis.call('GET','hello'),redis.call('GET','foo')} end,flags={'allow-cross-slot-keys'}}\n" +
		"redis.register_function{function_name='nocluster',callback=function(keys,args) return 1 end,flags={'no-cluster'}}"
	loadFunctionLibrary(t, s, code)

	_, err := s.Execute([][]byte{[]byte("FCALL"), []byte("blocked"), []byte("0")})
	if err == nil || !strings.Contains(err.Error(), "Script attempted to access a non local key in a cluster node") {
		t.Fatalf("default FCALL cross-slot err=%v", err)
	}

	if got := execute(t, s, "FCALL", "allowed", "0"); got != "*2\r\n$1\r\nA\r\n$1\r\nB\r\n" {
		t.Fatalf("allow-cross-slot FCALL=%q", got)
	}

	_, err = s.Execute([][]byte{[]byte("FCALL"), []byte("nocluster"), []byte("0")})
	if err == nil || !strings.Contains(err.Error(), "no-cluster") {
		t.Fatalf("no-cluster FCALL err=%v", err)
	}
}

func TestClusterEvalROUsesSameRuntimeLocality(t *testing.T) {
	s := newClusterScriptingServer(t, map[string]string{
		"0-16383": "127.0.0.1:7000",
	})
	execute(t, s, "SET", "hello", "A")
	execute(t, s, "SET", "foo", "B")

	flagged := "#!lua flags=no-writes\nreturn {redis.call('GET','hello'),redis.call('GET','foo')}"
	_, err := s.Execute([][]byte{[]byte("EVAL_RO"), []byte(flagged), []byte("0")})
	if err == nil || !strings.Contains(err.Error(), "Script attempted to access a non local key in a cluster node") {
		t.Fatalf("EVAL_RO cross-slot err=%v", err)
	}

	allowed := "#!lua flags=no-writes,allow-cross-slot-keys\nreturn {redis.call('GET','hello'),redis.call('GET','foo')}"
	if got := execute(t, s, "EVAL_RO", allowed, "0"); got != "*2\r\n$1\r\nA\r\n$1\r\nB\r\n" {
		t.Fatalf("allow-cross-slot EVAL_RO=%q", got)
	}
}
