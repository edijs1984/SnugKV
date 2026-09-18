package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func aclTestArgs(parts ...string) [][]byte {
	args := make([][]byte, len(parts))
	for i := range parts {
		args[i] = []byte(parts[i])
	}
	return args
}

func executeAuthorizedForTest(
	t *testing.T,
	s *Server,
	session *authSession,
	parts ...string,
) (string, error) {
	t.Helper()

	args := aclTestArgs(parts...)

	if err := s.authorizeConnectionCommand(session, args); err != nil {
		return "", err
	}

	response, err := s.executeForSession(args, session)
	return string(response), err
}

func newACLTestSession(
	t *testing.T,
	s *Server,
	name string,
	rules ...string,
) *authSession {
	t.Helper()

	if err := s.acl.SetUser(name, rules); err != nil {
		t.Fatalf("ACL SETUSER %s: %v", name, err)
	}

	return &authSession{
		username:      name,
		authenticated: true,
	}
}

func TestScriptNestedCommandACLIsReauthorized(t *testing.T) {
	s := New(engine.New())

	session := newACLTestSession(
		t,
		s,
		"script-command",
		"on",
		"nopass",
		"resetkeys",
		"~*",
		"nocommands",
		"+eval",
	)

	_, err := executeAuthorizedForTest(
		t,
		s,
		session,
		"EVAL",
		"return redis.call('GET','allowed:key')",
		"0",
	)

	if err == nil || !strings.Contains(
		err.Error(),
		"no permissions to run the 'get' command",
	) {
		t.Fatalf("nested GET ACL error = %v", err)
	}
}

func TestScriptNestedKeyACLIsReauthorized(t *testing.T) {
	s := New(engine.New())

	session := newACLTestSession(
		t,
		s,
		"script-key",
		"on",
		"nopass",
		"resetkeys",
		"~allowed:*",
		"nocommands",
		"+eval",
		"+get",
	)

	_, err := executeAuthorizedForTest(
		t,
		s,
		session,
		"EVAL",
		"return redis.call('GET','denied:key')",
		"0",
	)

	if err == nil || !strings.Contains(
		err.Error(),
		"NOPERM No permissions to access a key",
	) {
		t.Fatalf("nested denied-key ACL error = %v", err)
	}
}

func TestReadOnlyScriptNestedACLIsReauthorized(t *testing.T) {
	s := New(engine.New())

	session := newACLTestSession(
		t,
		s,
		"script-ro",
		"on",
		"nopass",
		"resetkeys",
		"~allowed:*",
		"nocommands",
		"+eval_ro",
		"+get",
	)

	_, err := executeAuthorizedForTest(
		t,
		s,
		session,
		"EVAL_RO",
		"return redis.call('GET','denied:key')",
		"0",
	)

	if err == nil || !strings.Contains(
		err.Error(),
		"NOPERM No permissions to access a key",
	) {
		t.Fatalf("nested EVAL_RO ACL error = %v", err)
	}
}

func TestFunctionNestedACLIsReauthorized(t *testing.T) {
	s := New(engine.New())

	loadFunctionLibrary(
		t,
		s,
		"#!lua name=acltest\n"+
			"redis.register_function('read_denied', function(keys,args) return redis.call('GET','denied:key') end)",
	)

	session := newACLTestSession(
		t,
		s,
		"function-key",
		"on",
		"nopass",
		"resetkeys",
		"~allowed:*",
		"nocommands",
		"+fcall",
		"+get",
	)

	_, err := executeAuthorizedForTest(
		t,
		s,
		session,
		"FCALL",
		"read_denied",
		"0",
	)

	if err == nil || !strings.Contains(
		err.Error(),
		"NOPERM No permissions to access a key",
	) {
		t.Fatalf("nested FCALL ACL error = %v", err)
	}
}

func TestSortDynamicKeyACLRequiresSameRuleSet(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "RPUSH", "src", "1", "2")
	execute(t, s, "SET", "weight_1", "2")
	execute(t, s, "SET", "weight_2", "1")

	session := newACLTestSession(
		t,
		s,
		"sort-split-selector",
		"on",
		"nopass",
		"resetkeys",
		"~src",
		"nocommands",
		"+sort",
		"(~weight_* +sort)",
	)

	_, err := executeAuthorizedForTest(
		t,
		s,
		session,
		"SORT",
		"src",
		"BY",
		"weight_*",
	)

	if err == nil || err.Error() != "NOPERM No permissions to access a key" {
		t.Fatalf("SORT split-selector ACL error = %v", err)
	}
}

func TestSortDynamicKeyACLDeniesPartialCompleteSelector(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "RPUSH", "src", "1", "2")
	execute(t, s, "SET", "weight_1", "2")
	execute(t, s, "SET", "weight_2", "1")
	execute(t, s, "SET", "name_1", "one")
	execute(t, s, "SET", "name_2", "two")

	session := newACLTestSession(
		t,
		s,
		"sort-complete-selector",
		"on",
		"nopass",
		"resetkeys",
		"nocommands",
		"(~src ~weight_* ~name_* +sort)",
	)

	_, err := executeAuthorizedForTest(
		t,
		s,
		session,
		"SORT",
		"src",
		"BY",
		"weight_*",
		"GET",
		"name_*",
	)

	if err == nil || err.Error() != "ERR BY option of SORT denied due to insufficient ACL permissions." {
		t.Fatalf("SORT partial complete-selector ACL error = %v", err)
	}
}

func TestSortDynamicKeyACLAllowsAllKeysSelector(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "RPUSH", "src", "1", "2")
	execute(t, s, "SET", "weight_1", "2")
	execute(t, s, "SET", "weight_2", "1")
	execute(t, s, "SET", "name_1", "one")
	execute(t, s, "SET", "name_2", "two")

	session := newACLTestSession(
		t,
		s,
		"sort-allkeys-selector",
		"on",
		"nopass",
		"resetkeys",
		"nocommands",
		"(~* +sort)",
	)

	got, err := executeAuthorizedForTest(
		t,
		s,
		session,
		"SORT",
		"src",
		"BY",
		"weight_*",
		"GET",
		"name_*",
	)

	if err != nil {
		t.Fatalf("SORT allkeys selector error = %v", err)
	}

	want := "*2\r\n$3\r\ntwo\r\n$3\r\none\r\n"
	if got != want {
		t.Fatalf("SORT allkeys selector = %q, want %q", got, want)
	}
}

func TestSortDynamicACLInsideLuaUsesNestedSortContext(t *testing.T) {
	s := New(engine.New())

	execute(t, s, "RPUSH", "src", "1", "2")
	execute(t, s, "SET", "weight_1", "2")
	execute(t, s, "SET", "weight_2", "1")

	session := newACLTestSession(
		t,
		s,
		"script-sort",
		"on",
		"nopass",
		"resetkeys",
		"~src",
		"nocommands",
		"+eval",
		"+sort",
	)

	_, err := executeAuthorizedForTest(
		t,
		s,
		session,
		"EVAL",
		"return redis.call('SORT','src','BY','weight_*')",
		"0",
	)

	if err == nil || !strings.Contains(
		err.Error(),
		"NOPERM No permissions to access a key",
	) {
		t.Fatalf("nested SORT dynamic-key ACL error = %v", err)
	}
}