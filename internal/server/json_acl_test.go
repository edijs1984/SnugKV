package server

import (
	"strings"
	"testing"
)

func TestACLJSONCategoryContainsEveryImplementedJSONCommand(t *testing.T) {
	got, ok := aclCommandsForCategory("json")
	if !ok {
		t.Fatal("json ACL category missing")
	}

	gotSet := make(map[string]struct{}, len(got))
	for _, command := range got {
		gotSet[command] = struct{}{}
	}

	want := []string{
		"json.set",
		"json.get",
		"json.type",
		"json.del",
		"json.numincrby",
		"json.strlen",
		"json.arrlen",
		"json.objlen",
		"json.arrappend",
		"json.strappend",
		"json.objkeys",
		"json.toggle",
		"json.arrpop",
		"json.arrinsert",
		"json.arrindex",
		"json.clear",
		"json.arrtrim",
		"json.mget",
		"json.merge",
		"json.mset",
		"json.forget",
	}

	for _, command := range want {
		if _, ok := gotSet[command]; !ok {
			t.Errorf("%s missing from @json", command)
		}
	}

	for _, command := range got {
		if !strings.HasPrefix(command, "json.") {
			t.Errorf("non-JSON command %q leaked into @json", command)
		}
	}
}

func TestACLJSONReadWriteCategoryClassification(t *testing.T) {
	readCommands := []string{
		"json.get",
		"json.type",
		"json.strlen",
		"json.arrlen",
		"json.objlen",
		"json.objkeys",
		"json.arrindex",
		"json.mget",
	}
	writeCommands := []string{
		"json.set",
		"json.del",
		"json.numincrby",
		"json.arrappend",
		"json.strappend",
		"json.toggle",
		"json.arrpop",
		"json.arrinsert",
		"json.clear",
		"json.arrtrim",
		"json.merge",
		"json.mset",
		"json.forget",
	}

	read, ok := aclCommandsForCategory("read")
	if !ok {
		t.Fatal("read ACL category missing")
	}
	write, ok := aclCommandsForCategory("write")
	if !ok {
		t.Fatal("write ACL category missing")
	}

	readSet := make(map[string]struct{}, len(read))
	for _, command := range read {
		readSet[command] = struct{}{}
	}
	writeSet := make(map[string]struct{}, len(write))
	for _, command := range write {
		writeSet[command] = struct{}{}
	}

	for _, command := range readCommands {
		if _, ok := readSet[command]; !ok {
			t.Errorf("%s missing from @read", command)
		}
		if _, ok := writeSet[command]; ok {
			t.Errorf("%s incorrectly present in @write", command)
		}
	}
	for _, command := range writeCommands {
		if _, ok := writeSet[command]; !ok {
			t.Errorf("%s missing from @write", command)
		}
		if _, ok := readSet[command]; ok {
			t.Errorf("%s incorrectly present in @read", command)
		}
	}
}

func TestACLJSONCategoryGrantsOnlyJSONCommands(t *testing.T) {
	acl := NewACL()
	if err := acl.SetUser("jsonuser", []string{
		"on",
		"nopass",
		"-@all",
		"+@json",
		"allkeys",
	}); err != nil {
		t.Fatal(err)
	}

	for _, command := range []string{
		"json.get",
		"json.set",
		"json.mget",
		"json.mset",
		"json.arrindex",
		"json.arrappend",
	} {
		if !acl.CommandAllowed("jsonuser", command) {
			t.Errorf("%s should be allowed by +@json", command)
		}
	}

	for _, command := range []string{"get", "set", "hget", "del"} {
		if acl.CommandAllowed("jsonuser", command) {
			t.Errorf("%s should not be allowed by +@json", command)
		}
	}
}

func TestACLJSONReadAndWriteRulesAreIndependent(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("jsonreader", []string{
		"on",
		"nopass",
		"-@all",
		"+@read",
		"allkeys",
	}); err != nil {
		t.Fatal(err)
	}
	if !acl.CommandAllowed("jsonreader", "json.get") {
		t.Fatal("json.get should be allowed by +@read")
	}
	if acl.CommandAllowed("jsonreader", "json.set") {
		t.Fatal("json.set should not be allowed by +@read")
	}

	if err := acl.SetUser("jsonwriter", []string{
		"on",
		"nopass",
		"-@all",
		"+@write",
		"allkeys",
	}); err != nil {
		t.Fatal(err)
	}
	if !acl.CommandAllowed("jsonwriter", "json.set") {
		t.Fatal("json.set should be allowed by +@write")
	}
	if acl.CommandAllowed("jsonwriter", "json.get") {
		t.Fatal("json.get should not be allowed by +@write")
	}
}

func TestACLAuthorizeJSONMGetChecksEveryKey(t *testing.T) {
	acl := NewACL()
	if err := acl.SetUser("jsonreader", []string{
		"on",
		"nopass",
		"+json.mget",
		"resetkeys",
		"~allowed:*",
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{acl: acl}

	if err := s.authorizeCommandKeys("jsonreader", [][]byte{
		[]byte("JSON.MGET"),
		[]byte("allowed:1"),
		[]byte("allowed:2"),
		[]byte("$.name"),
	}); err != nil {
		t.Fatalf("allowed JSON.MGET rejected: %v", err)
	}

	err := s.authorizeCommandKeys("jsonreader", [][]byte{
		[]byte("JSON.MGET"),
		[]byte("allowed:1"),
		[]byte("denied:2"),
		[]byte("$.name"),
	})
	if err == nil {
		t.Fatal("JSON.MGET containing denied key was allowed")
	}
	if got := err.Error(); got != "NOPERM No permissions to access a key" {
		t.Fatalf("unexpected error: %q", got)
	}
}

func TestACLAuthorizeJSONMSetChecksEveryKey(t *testing.T) {
	acl := NewACL()
	if err := acl.SetUser("jsonwriter", []string{
		"on",
		"nopass",
		"+json.mset",
		"resetkeys",
		"~allowed:*",
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{acl: acl}

	if err := s.authorizeCommandKeys("jsonwriter", [][]byte{
		[]byte("JSON.MSET"),
		[]byte("allowed:1"),
		[]byte("$"),
		[]byte(`{"v":1}`),
		[]byte("allowed:2"),
		[]byte("$"),
		[]byte(`{"v":2}`),
	}); err != nil {
		t.Fatalf("allowed JSON.MSET rejected: %v", err)
	}

	err := s.authorizeCommandKeys("jsonwriter", [][]byte{
		[]byte("JSON.MSET"),
		[]byte("allowed:1"),
		[]byte("$"),
		[]byte(`{"v":1}`),
		[]byte("denied:2"),
		[]byte("$"),
		[]byte(`{"v":2}`),
	})
	if err == nil {
		t.Fatal("JSON.MSET containing denied key was allowed")
	}
	if got := err.Error(); got != "NOPERM No permissions to access a key" {
		t.Fatalf("unexpected error: %q", got)
	}
}
