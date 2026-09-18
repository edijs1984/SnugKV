package server

import (
	"os"
	"path/filepath"
	"snugkv/internal/config"
	"snugkv/internal/engine"
	"strings"
	"testing"
	"time"
)

func TestACLDefaultUser(t *testing.T) {
	acl := NewACL()

	u, ok := acl.GetUser("default")
	if !ok {
		t.Fatal("default user missing")
	}

	if !u.Enabled {
		t.Fatal("default user must be enabled")
	}

	if !u.NoPass {
		t.Fatal("default user must start nopass")
	}

	if !u.AllCommands {
		t.Fatal("default user must allow all commands")
	}

	if !u.AllKeys {
		t.Fatal("default user must allow all keys")
	}
}

func TestACLPasswordHashMatchesRedisAudit(t *testing.T) {
	const want = "c2cb2efd78983b299d2478eb602351cfba7ea59a53fc080853f8542bfa7537e2"

	got := aclPasswordHash("snug-secret-123")

	if got != want {
		t.Fatalf("hash mismatch: got %q want %q", got, want)
	}
}

func TestACLCreateUser(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("snugtest", nil); err != nil {
		t.Fatal(err)
	}

	u, ok := acl.GetUser("snugtest")
	if !ok {
		t.Fatal("user missing")
	}

	if u.Enabled {
		t.Fatal("new user must start disabled")
	}

	if u.AllCommands {
		t.Fatal("new user must start with -@all")
	}

	if len(u.KeyPatterns) != 0 {
		t.Fatalf("expected no key patterns, got %#v", u.KeyPatterns)
	}
}

func TestACLConfigureAndAuthenticateUser(t *testing.T) {
	acl := NewACL()

	err := acl.SetUser("snugtest", []string{
		"on",
		">snug-secret-123",
		"allkeys",
		"+get",
	})
	if err != nil {
		t.Fatal(err)
	}

	if !acl.Authenticate("snugtest", "snug-secret-123") {
		t.Fatal("correct password rejected")
	}

	if acl.Authenticate("snugtest", "wrong-password") {
		t.Fatal("wrong password accepted")
	}

	if !acl.CommandAllowed("snugtest", "get") {
		t.Fatal("GET should be allowed")
	}

	if acl.CommandAllowed("snugtest", "set") {
		t.Fatal("SET should be denied")
	}
}

func TestACLKeyPatterns(t *testing.T) {
	acl := NewACL()

	err := acl.SetUser("snugtest", []string{
		"on",
		"nopass",
		"+get",
		"resetkeys",
		"~allowed:*",
	})
	if err != nil {
		t.Fatal(err)
	}

	if !acl.KeyAllowed("snugtest", "allowed:key") {
		t.Fatal("allowed:key should be permitted")
	}

	if acl.KeyAllowed("snugtest", "denied:key") {
		t.Fatal("denied:key should be rejected")
	}
}

func TestACLWhoamiCommandPermission(t *testing.T) {
	acl := NewACL()

	err := acl.SetUser("snugtest", []string{
		"on",
		"nopass",
		"+get",
		"+acl|whoami",
	})
	if err != nil {
		t.Fatal(err)
	}

	if !acl.CommandAllowed("snugtest", "acl|whoami") {
		t.Fatal("acl|whoami should be allowed")
	}
}

func TestACLDisableUser(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("snugtest", []string{
		"on",
		">secret",
	}); err != nil {
		t.Fatal(err)
	}

	if !acl.Authenticate("snugtest", "secret") {
		t.Fatal("expected authentication success")
	}

	if err := acl.SetUser("snugtest", []string{"off"}); err != nil {
		t.Fatal(err)
	}

	if acl.Authenticate("snugtest", "secret") {
		t.Fatal("disabled user authenticated")
	}
}

func TestACLDeleteUser(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("snugtest", nil); err != nil {
		t.Fatal(err)
	}

	if got := acl.DeleteUsers("snugtest"); got != 1 {
		t.Fatalf("deleted %d users, want 1", got)
	}

	if _, ok := acl.GetUser("snugtest"); ok {
		t.Fatal("user still exists")
	}
}

func TestACLGlob(t *testing.T) {
	tests := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"*", "anything", true},
		{"allowed:*", "allowed:key", true},
		{"allowed:*", "denied:key", false},
		{"foo?bar", "foo1bar", true},
		{"foo?bar", "foobar", false},
	}

	for _, tc := range tests {
		if got := aclGlobMatch(tc.pattern, tc.value); got != tc.want {
			t.Fatalf(
				"aclGlobMatch(%q, %q) = %v want %v",
				tc.pattern,
				tc.value,
				got,
				tc.want,
			)
		}
	}
}

func TestACLAuthorizeCommandKeys(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("snugtest", []string{
		"on",
		"nopass",
		"+get",
		"+set",
		"+mget",
		"resetkeys",
		"~allowed:*",
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{acl: acl}

	if err := s.authorizeCommandKeys(
		"snugtest",
		[][]byte{
			[]byte("GET"),
			[]byte("allowed:key"),
		},
	); err != nil {
		t.Fatalf("allowed GET rejected: %v", err)
	}

	if err := s.authorizeCommandKeys(
		"snugtest",
		[][]byte{
			[]byte("GET"),
			[]byte("denied:key"),
		},
	); err == nil {
		t.Fatal("denied GET was allowed")
	}
}

func TestACLAuthorizeAllKeysInMultiKeyCommand(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("snugtest", []string{
		"on",
		"nopass",
		"+mget",
		"resetkeys",
		"~allowed:*",
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{acl: acl}

	err := s.authorizeCommandKeys(
		"snugtest",
		[][]byte{
			[]byte("MGET"),
			[]byte("allowed:1"),
			[]byte("denied:1"),
			[]byte("allowed:2"),
		},
	)

	if err == nil {
		t.Fatal("MGET containing denied key was allowed")
	}

	if got := err.Error(); got != "NOPERM No permissions to access a key" {
		t.Fatalf("unexpected error: %q", got)
	}
}

func TestACLAuthorizeMSetKeys(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("snugtest", []string{
		"on",
		"nopass",
		"+mset",
		"resetkeys",
		"~allowed:*",
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{acl: acl}

	if err := s.authorizeCommandKeys(
		"snugtest",
		[][]byte{
			[]byte("MSET"),
			[]byte("allowed:1"),
			[]byte("one"),
			[]byte("allowed:2"),
			[]byte("two"),
		},
	); err != nil {
		t.Fatalf("allowed MSET rejected: %v", err)
	}

	err := s.authorizeCommandKeys(
		"snugtest",
		[][]byte{
			[]byte("MSET"),
			[]byte("allowed:1"),
			[]byte("one"),
			[]byte("denied:2"),
			[]byte("two"),
		},
	)

	if err == nil {
		t.Fatal("MSET containing denied key was allowed")
	}
}

func TestACLAuthorizeRenameChecksBothKeys(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("snugtest", []string{
		"on",
		"nopass",
		"+rename",
		"resetkeys",
		"~allowed:*",
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{acl: acl}

	err := s.authorizeCommandKeys(
		"snugtest",
		[][]byte{
			[]byte("RENAME"),
			[]byte("allowed:source"),
			[]byte("denied:destination"),
		},
	)

	if err == nil {
		t.Fatal("RENAME with denied destination was allowed")
	}
}

func TestACLFailureMarksTransactionDirty(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("snugtest", []string{
		"on",
		"nopass",
		"+multi",
		"+exec",
		"+set",
		"resetkeys",
		"~allowed:*",
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{acl: acl}
	auth := &authSession{
		username:      "snugtest",
		authenticated: true,
	}

	tx := newTransactionSession(s)
	tx.auth = auth

	tx.multi = true

	err := s.authorizeConnectionCommand(
		auth,
		[][]byte{
			[]byte("SET"),
			[]byte("denied:key"),
			[]byte("value"),
		},
	)

	if err == nil {
		t.Fatal("expected ACL denial")
	}

	tx.markACLFailure()

	if !tx.queueDirty {
		t.Fatal("ACL failure did not mark transaction dirty")
	}
}

func TestACLQueuedCommandRecheckedAtExec(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("snugtest", []string{
		"on",
		"nopass",
		"+multi",
		"+exec",
		"+set",
		"allkeys",
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{acl: acl}

	auth := &authSession{
		username:      "snugtest",
		authenticated: true,
	}

	tx := newTransactionSession(s)
	tx.auth = auth
	tx.multi = true

	if _, err := tx.queueCommand(
		[][]byte{
			[]byte("SET"),
			[]byte("allowed:key"),
			[]byte("value"),
		},
	); err != nil {
		t.Fatal(err)
	}

	if err := acl.SetUser("snugtest", []string{"-set"}); err != nil {
		t.Fatal(err)
	}

	_, err := tx.exec()

	if err == nil {
		t.Fatal("EXEC should reject command after ACL permission removal")
	}

	const want = "NOPERM ACLs rules changed between the moment the transaction was accumulated and the EXEC call. This command is no longer allowed for the following reason: no permission to execute the command or subcommand"

	if err.Error() != want {
		t.Fatalf("got %q want %q", err.Error(), want)
	}
}

func TestACLDryRunAllowed(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("snugdry", []string{
		"on",
		"nopass",
		"+get",
		"resetkeys",
		"~allowed:*",
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{acl: acl}

	got, err := s.executeACLDryRun([][]byte{
		[]byte("ACL"),
		[]byte("DRYRUN"),
		[]byte("snugdry"),
		[]byte("GET"),
		[]byte("allowed:key"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != "+OK\r\n" {
		t.Fatalf("got %q", got)
	}
}

func TestACLDryRunDeniedCommand(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("snugdry", []string{
		"on",
		"nopass",
		"+get",
		"allkeys",
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{acl: acl}

	got, err := s.executeACLDryRun([][]byte{
		[]byte("ACL"),
		[]byte("DRYRUN"),
		[]byte("snugdry"),
		[]byte("SET"),
		[]byte("allowed:key"),
		[]byte("value"),
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "$56\r\nUser snugdry has no permissions to run the 'set' command\r\n"

	if string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestACLDryRunDeniedKey(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("snugdry", []string{
		"on",
		"nopass",
		"+get",
		"resetkeys",
		"~allowed:*",
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{acl: acl}

	got, err := s.executeACLDryRun([][]byte{
		[]byte("ACL"),
		[]byte("DRYRUN"),
		[]byte("snugdry"),
		[]byte("GET"),
		[]byte("denied:key"),
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "$62\r\nUser snugdry has no permissions to access the 'denied:key' key\r\n"

	if string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestACLDryRunDisabledUserStillEvaluatesRules(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("snugdry", []string{
		"off",
		"nopass",
		"+get",
		"allkeys",
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{acl: acl}

	got, err := s.executeACLDryRun([][]byte{
		[]byte("ACL"),
		[]byte("DRYRUN"),
		[]byte("snugdry"),
		[]byte("GET"),
		[]byte("allowed:key"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != "+OK\r\n" {
		t.Fatalf("got %q", got)
	}
}

func TestACLDryRunMissingUser(t *testing.T) {
	s := &Server{acl: NewACL()}

	_, err := s.executeACLDryRun([][]byte{
		[]byte("ACL"),
		[]byte("DRYRUN"),
		[]byte("missing"),
		[]byte("GET"),
		[]byte("key"),
	})

	if err == nil {
		t.Fatal("expected missing-user error")
	}

	if got, want := err.Error(), "ERR User 'missing' not found"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestACLGenPassLengths(t *testing.T) {
	tests := []struct {
		args [][]byte
		want int
	}{
		{
			args: [][]byte{
				[]byte("ACL"),
				[]byte("GENPASS"),
			},
			want: 64,
		},
		{
			args: [][]byte{
				[]byte("ACL"),
				[]byte("GENPASS"),
				[]byte("1"),
			},
			want: 1,
		},
		{
			args: [][]byte{
				[]byte("ACL"),
				[]byte("GENPASS"),
				[]byte("4"),
			},
			want: 1,
		},
		{
			args: [][]byte{
				[]byte("ACL"),
				[]byte("GENPASS"),
				[]byte("8"),
			},
			want: 2,
		},
		{
			args: [][]byte{
				[]byte("ACL"),
				[]byte("GENPASS"),
				[]byte("16"),
			},
			want: 4,
		},
	}

	for _, tc := range tests {
		reply, err := executeACLGenPass(tc.args)
		if err != nil {
			t.Fatal(err)
		}

		// RESP bulk:
		// $<length>\r\n<payload>\r\n
		parts := strings.Split(string(reply), "\r\n")
		if len(parts) < 3 {
			t.Fatalf("invalid bulk reply %q", reply)
		}

		payload := parts[1]

		if len(payload) != tc.want {
			t.Fatalf(
				"GENPASS payload length=%d want=%d payload=%q",
				len(payload),
				tc.want,
				payload,
			)
		}

		for _, c := range payload {
			if !strings.ContainsRune("0123456789abcdef", c) {
				t.Fatalf("non-hex GENPASS output %q", payload)
			}
		}
	}
}

func TestACLGenPassInvalidBits(t *testing.T) {
	for _, bits := range []string{"0", "-1", "4097"} {
		_, err := executeACLGenPass([][]byte{
			[]byte("ACL"),
			[]byte("GENPASS"),
			[]byte(bits),
		})

		if err == nil {
			t.Fatalf("GENPASS %s unexpectedly succeeded", bits)
		}

		want := "ERR ACL GENPASS argument must be the number of bits for the output password, a positive number up to 4096"

		if err.Error() != want {
			t.Fatalf(
				"GENPASS %s got %q want %q",
				bits,
				err.Error(),
				want,
			)
		}
	}
}

func TestACLCatAll(t *testing.T) {
	reply, err := aclCategoryReply("")
	if err != nil {
		t.Fatal(err)
	}

	text := string(reply)

	for _, category := range []string{
		"keyspace",
		"read",
		"write",
		"string",
		"hash",
		"list",
		"set",
		"sortedset",
		"stream",
		"scripting",
		"json",
	} {
		if !strings.Contains(text, category) {
			t.Fatalf("missing category %q", category)
		}
	}
}

func TestACLCatUnknown(t *testing.T) {
	_, err := aclCategoryReply("does-not-exist")

	if err == nil {
		t.Fatal("expected error")
	}

	want := "ERR Unknown category 'does-not-exist'"

	if err.Error() != want {
		t.Fatalf("got %q want %q", err.Error(), want)
	}
}

func TestACLCatStringContainsGET(t *testing.T) {
	reply, err := aclCategoryReply("string")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(reply), "get") {
		t.Fatalf("GET missing from string category: %q", reply)
	}
}

func TestACLCatTransaction(t *testing.T) {
	reply, err := aclCategoryReply("transaction")
	if err != nil {
		t.Fatal(err)
	}

	text := string(reply)

	for _, command := range []string{
		"multi",
		"exec",
		"discard",
		"watch",
		"unwatch",
	} {
		if !strings.Contains(text, command) {
			t.Fatalf("%s missing from transaction category", command)
		}
	}
}

func TestACLCatWriteDoesNotContainGET(t *testing.T) {
	reply, err := aclCategoryReply("write")
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(reply), "\r\nget\r\n") {
		t.Fatal("GET unexpectedly classified as write")
	}
}

func TestACLCatReadDoesNotExposeNonReadCommands(t *testing.T) {
	reply, err := aclCategoryReply("read")
	if err != nil {
		t.Fatal(err)
	}

	text := string(reply)

	for _, command := range []string{
		"info",
		"memory",
		"publish",
		"subscribe",
		"psubscribe",
		"pubsub",
		"function",
		"script",
		"eval_ro",
		"evalsha_ro",
		"fcall_ro",
	} {
		needle := "\r\n" + command + "\r\n"

		if strings.Contains(text, needle) {
			t.Fatalf(
				"%s unexpectedly classified as read",
				command,
			)
		}
	}
}

func TestACLCatWriteDoesNotExposeScriptingCommands(t *testing.T) {
	reply, err := aclCategoryReply("write")
	if err != nil {
		t.Fatal(err)
	}

	text := string(reply)

	for _, command := range []string{
		"eval",
		"evalsha",
		"fcall",
		"xgroup",
	} {
		needle := "\r\n" + command + "\r\n"

		if strings.Contains(text, needle) {
			t.Fatalf(
				"%s unexpectedly classified as write",
				command,
			)
		}
	}
}

func TestACLCatScriptingDoesNotExposeUmbrellaCommands(t *testing.T) {
	reply, err := aclCategoryReply("scripting")
	if err != nil {
		t.Fatal(err)
	}

	text := string(reply)

	for _, command := range []string{
		"function",
		"script",
	} {
		needle := "\r\n" + command + "\r\n"

		if strings.Contains(text, needle) {
			t.Fatalf(
				"%s unexpectedly exposed in scripting category",
				command,
			)
		}
	}
}

func TestACLCatSnugCommandsNotMisclassified(t *testing.T) {
	for _, category := range []string{
		"read",
		"write",
		"fast",
		"slow",
		"dangerous",
	} {
		reply, err := aclCategoryReply(category)
		if err != nil {
			t.Fatal(err)
		}

		if strings.Contains(
			strings.ToLower(string(reply)),
			"snug.",
		) {
			t.Fatalf(
				"Snug-specific command leaked into Redis category %s",
				category,
			)
		}
	}
}

func TestACLLogEmpty(t *testing.T) {
	log := NewACLLog()

	reply := aclLogReply(log.Entries(10))

	if string(reply) != "*0\r\n" {
		t.Fatalf("got %q", reply)
	}
}

func TestACLLogNewestFirst(t *testing.T) {
	log := NewACLLog()

	log.Add(
		"auth",
		"AUTH",
		"user1",
		"client-one",
	)

	log.Add(
		"command",
		"set",
		"user2",
		"client-two",
	)

	entries := log.Entries(10)

	if len(entries) != 2 {
		t.Fatalf("got %d entries", len(entries))
	}

	if entries[0].Reason != "command" {
		t.Fatalf(
			"newest reason=%q",
			entries[0].Reason,
		)
	}

	if entries[1].Reason != "auth" {
		t.Fatalf(
			"oldest reason=%q",
			entries[1].Reason,
		)
	}
}

func TestACLLogLimit(t *testing.T) {
	log := NewACLLog()

	log.Add("auth", "AUTH", "a", "")
	log.Add("command", "set", "b", "")
	log.Add("key", "secret", "c", "")

	entries := log.Entries(2)

	if len(entries) != 2 {
		t.Fatalf("got %d entries", len(entries))
	}

	if entries[0].Reason != "key" {
		t.Fatalf(
			"first=%q",
			entries[0].Reason,
		)
	}

	if entries[1].Reason != "command" {
		t.Fatalf(
			"second=%q",
			entries[1].Reason,
		)
	}
}

func TestACLLogZeroAndNegative(t *testing.T) {
	log := NewACLLog()

	log.Add("auth", "AUTH", "user", "")

	if got := log.Entries(0); len(got) != 0 {
		t.Fatalf("LOG 0 returned %d entries", len(got))
	}

	if got := log.Entries(-1); len(got) != 0 {
		t.Fatalf(
			"LOG -1 returned %d entries",
			len(got),
		)
	}
}

func TestACLLogReset(t *testing.T) {
	log := NewACLLog()

	log.Add("auth", "AUTH", "user", "")

	log.Reset()

	if got := log.Entries(10); len(got) != 0 {
		t.Fatalf(
			"got %d entries after reset",
			len(got),
		)
	}
}

func TestACLLogRESPShape(t *testing.T) {
	log := NewACLLog()

	log.Add(
		"key",
		"denied:key",
		"loguser",
		"id=1 cmd=get user=loguser",
	)

	reply := string(
		aclLogReply(log.Entries(1)),
	)

	if !strings.HasPrefix(reply, "*1\r\n*20\r\n") {
		t.Fatalf(
			"unexpected RESP shape %q",
			reply,
		)
	}

	for _, field := range []string{
		"count",
		"reason",
		"key",
		"context",
		"toplevel",
		"object",
		"denied:key",
		"username",
		"loguser",
		"age-seconds",
		"client-info",
		"entry-id",
		"timestamp-created",
		"timestamp-last-updated",
	} {
		if !strings.Contains(reply, field) {
			t.Fatalf(
				"missing %q in %q",
				field,
				reply,
			)
		}
	}
}

func TestACLLogAggregatesEquivalentEntries(t *testing.T) {
	log := NewACLLog()

	log.Add(
		"command",
		"set",
		"logagg",
		"client-one",
	)

	first := log.Entries(10)

	if len(first) != 1 {
		t.Fatalf("initial entries=%d", len(first))
	}

	firstID := first[0].EntryID
	firstCreated := first[0].TimestampCreated

	time.Sleep(time.Millisecond)

	log.Add(
		"command",
		"set",
		"logagg",
		"client-two",
	)

	time.Sleep(time.Millisecond)

	log.Add(
		"command",
		"set",
		"logagg",
		"client-three",
	)

	entries := log.Entries(10)

	if len(entries) != 1 {
		t.Fatalf(
			"expected aggregation into 1 entry, got %d",
			len(entries),
		)
	}

	entry := entries[0]

	if entry.Count != 3 {
		t.Fatalf(
			"count=%d want=3",
			entry.Count,
		)
	}

	if entry.EntryID != firstID {
		t.Fatalf(
			"entry id changed: got=%d want=%d",
			entry.EntryID,
			firstID,
		)
	}

	if entry.TimestampCreated != firstCreated {
		t.Fatalf(
			"timestamp-created changed: got=%d want=%d",
			entry.TimestampCreated,
			firstCreated,
		)
	}

	if entry.TimestampLastUpdated <= firstCreated {
		t.Fatalf(
			"timestamp-last-updated=%d created=%d",
			entry.TimestampLastUpdated,
			firstCreated,
		)
	}

	if entry.ClientInfo != "client-three" {
		t.Fatalf(
			"client-info=%q want latest client",
			entry.ClientInfo,
		)
	}
}

func TestACLLogDifferentObjectsDoNotAggregate(t *testing.T) {
	log := NewACLLog()

	log.Add("key", "denied:a", "logagg", "a")
	log.Add("key", "denied:b", "logagg", "b")
	log.Add("key", "denied:c", "logagg", "c")

	entries := log.Entries(10)

	if len(entries) != 3 {
		t.Fatalf(
			"entries=%d want=3",
			len(entries),
		)
	}

	if entries[0].Object != "denied:c" ||
		entries[1].Object != "denied:b" ||
		entries[2].Object != "denied:a" {
		t.Fatalf(
			"wrong order: %#v",
			entries,
		)
	}

	for _, entry := range entries {
		if entry.Count != 1 {
			t.Fatalf(
				"%s count=%d",
				entry.Object,
				entry.Count,
			)
		}
	}
}

func TestACLLogAuthFailuresAggregate(t *testing.T) {
	log := NewACLLog()

	log.Add("auth", "AUTH", "logagg", "client-1")
	log.Add("auth", "AUTH", "logagg", "client-2")
	log.Add("auth", "AUTH", "logagg", "client-3")

	entries := log.Entries(10)

	if len(entries) != 1 {
		t.Fatalf(
			"entries=%d want=1",
			len(entries),
		)
	}

	if entries[0].Count != 3 {
		t.Fatalf(
			"count=%d want=3",
			entries[0].Count,
		)
	}

	if entries[0].ClientInfo != "client-3" {
		t.Fatalf(
			"client-info=%q",
			entries[0].ClientInfo,
		)
	}
}

func TestACLLogUpdatedEntryMovesToFront(t *testing.T) {
	log := NewACLLog()

	log.Add("command", "set", "user", "one")
	log.Add("key", "secret", "user", "two")

	// Update the older command entry.
	log.Add("command", "set", "user", "three")

	entries := log.Entries(10)

	if len(entries) != 2 {
		t.Fatalf("entries=%d", len(entries))
	}

	if entries[0].Reason != "command" ||
		entries[0].Object != "set" {
		t.Fatalf(
			"updated entry was not moved to front: %#v",
			entries,
		)
	}

	if entries[0].Count != 2 {
		t.Fatalf(
			"count=%d want=2",
			entries[0].Count,
		)
	}
}

func TestACLCategoryRead(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("reader", []string{
		"on",
		"nopass",
		"-@all",
		"+@read",
	}); err != nil {
		t.Fatal(err)
	}

	if !acl.CommandAllowed("reader", "get") {
		t.Fatal("GET should be allowed by +@read")
	}

	if !acl.CommandAllowed("reader", "mget") {
		t.Fatal("MGET should be allowed by +@read")
	}

	if acl.CommandAllowed("reader", "set") {
		t.Fatal("SET should not be allowed by +@read")
	}
}

func TestACLCategoryWrite(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("writer", []string{
		"on",
		"nopass",
		"-@all",
		"+@write",
	}); err != nil {
		t.Fatal(err)
	}

	if !acl.CommandAllowed("writer", "set") {
		t.Fatal("SET should be allowed by +@write")
	}

	if acl.CommandAllowed("writer", "get") {
		t.Fatal("GET should not be allowed by +@write")
	}
}

func TestACLCategoryString(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("strings", []string{
		"on",
		"nopass",
		"-@all",
		"+@string",
	}); err != nil {
		t.Fatal(err)
	}

	if !acl.CommandAllowed("strings", "get") {
		t.Fatal("GET should be allowed by +@string")
	}

	if !acl.CommandAllowed("strings", "set") {
		t.Fatal("SET should be allowed by +@string")
	}

	if acl.CommandAllowed("strings", "hget") {
		t.Fatal("HGET should not be allowed by +@string")
	}
}

func TestACLCategoryOrderStringThenMinusWrite(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("ordered", []string{
		"on",
		"nopass",
		"-@all",
		"+@string",
		"-@write",
	}); err != nil {
		t.Fatal(err)
	}

	if !acl.CommandAllowed("ordered", "get") {
		t.Fatal("GET should remain allowed")
	}

	if acl.CommandAllowed("ordered", "set") {
		t.Fatal("SET should be denied by later -@write")
	}

	if acl.CommandAllowed("ordered", "incr") {
		t.Fatal("INCR should be denied by later -@write")
	}
}

func TestACLCategoryOrderMinusWriteThenString(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("ordered", []string{
		"on",
		"nopass",
		"-@all",
		"-@write",
		"+@string",
	}); err != nil {
		t.Fatal(err)
	}

	if !acl.CommandAllowed("ordered", "get") {
		t.Fatal("GET should be allowed")
	}

	if !acl.CommandAllowed("ordered", "set") {
		t.Fatal("SET should be restored by later +@string")
	}

	if !acl.CommandAllowed("ordered", "incr") {
		t.Fatal("INCR should be restored by later +@string")
	}
}

func TestACLCategoryExplicitCommandOverridesCategory(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("reader", []string{
		"on",
		"nopass",
		"-@all",
		"+@read",
		"-get",
	}); err != nil {
		t.Fatal(err)
	}

	if acl.CommandAllowed("reader", "get") {
		t.Fatal("later -get should override +@read")
	}

	if !acl.CommandAllowed("reader", "mget") {
		t.Fatal("MGET should remain allowed")
	}
}

func TestACLCategoryExplicitCommandRestoresPermission(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("user", []string{
		"on",
		"nopass",
		"+@all",
		"-@read",
		"+get",
	}); err != nil {
		t.Fatal(err)
	}

	if !acl.CommandAllowed("user", "get") {
		t.Fatal("later +get should restore GET")
	}

	if acl.CommandAllowed("user", "mget") {
		t.Fatal("MGET should remain denied by -@read")
	}

	if !acl.CommandAllowed("user", "set") {
		t.Fatal("SET should remain allowed from +@all")
	}
}

func TestACLCategoryDangerousRemoval(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("safe", []string{
		"on",
		"nopass",
		"+@all",
		"-@dangerous",
	}); err != nil {
		t.Fatal(err)
	}

	if !acl.CommandAllowed("safe", "get") {
		t.Fatal("GET should remain allowed")
	}

	if acl.CommandAllowed("safe", "keys") {
		t.Fatal("KEYS should be denied by -@dangerous")
	}

	if acl.CommandAllowed("safe", "flushdb") {
		t.Fatal("FLUSHDB should be denied by -@dangerous")
	}
}

func TestACLCategoryCaseInsensitive(t *testing.T) {
	acl := NewACL()

	if err := acl.SetUser("reader", []string{
		"on",
		"nopass",
		"-@all",
		"+@READ",
	}); err != nil {
		t.Fatal(err)
	}

	if !acl.CommandAllowed("reader", "get") {
		t.Fatal("+@READ should allow GET")
	}
}

func TestACLInvalidCategory(t *testing.T) {
	acl := NewACL()

	err := acl.SetUser("invalid", []string{
		"on",
		"nopass",
		"-@all",
		"+@does-not-exist",
	})

	if err == nil {
		t.Fatal("expected invalid category error")
	}

	want := "ERR Error in ACL SETUSER modifier '+@does-not-exist': Unknown command or category name in ACL"

	if err.Error() != want {
		t.Fatalf(
			"got %q want %q",
			err.Error(),
			want,
		)
	}
}

func TestACLFileSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.acl")

	acl := NewACL()

	if err := acl.SetUser("persistuser", []string{
		"on",
		">persist-pass",
		"resetkeys",
		"~persist:*",
		"-@all",
		"+@read",
		"+set",
	}); err != nil {
		t.Fatal(err)
	}

	if err := acl.SaveFile(path); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	text := string(data)

	if !strings.Contains(
		text,
		"user persistuser on",
	) {
		t.Fatalf("missing user: %q", text)
	}

	if strings.Contains(text, "persist-pass") {
		t.Fatal("plaintext password written to ACL file")
	}

	if !strings.Contains(text, "#") {
		t.Fatal("password hash missing from ACL file")
	}

	acl.DeleteUsers("persistuser")

	if _, ok := acl.GetUser("persistuser"); ok {
		t.Fatal("user was not deleted")
	}

	if err := acl.LoadFile(path); err != nil {
		t.Fatal(err)
	}

	user, ok := acl.GetUser("persistuser")
	if !ok {
		t.Fatal("user was not restored")
	}

	if !user.Enabled {
		t.Fatal("restored user is disabled")
	}

	if !acl.Authenticate(
		"persistuser",
		"persist-pass",
	) {
		t.Fatal("restored password does not authenticate")
	}

	if !acl.CommandAllowed(
		"persistuser",
		"get",
	) {
		t.Fatal("restored +@read missing GET")
	}

	if !acl.CommandAllowed(
		"persistuser",
		"set",
	) {
		t.Fatal("restored +set missing SET")
	}

	if acl.CommandAllowed(
		"persistuser",
		"del",
	) {
		t.Fatal("unexpected DEL permission")
	}

	if !acl.KeyAllowed(
		"persistuser",
		"persist:key",
	) {
		t.Fatal("restored key pattern does not match")
	}

	if acl.KeyAllowed(
		"persistuser",
		"other:key",
	) {
		t.Fatal("restored key pattern is too broad")
	}
}

func TestACLFileLoadIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.acl")

	acl := NewACL()

	if err := acl.SetUser("existing", []string{
		"on",
		"nopass",
		"+@all",
		"~*",
	}); err != nil {
		t.Fatal(err)
	}

	data := []byte(
		"user default on nopass ~* &* +@all\n" +
			"user broken on #0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef ~* +@does-not-exist\n",
	)

	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	err := acl.LoadFile(path)
	if err == nil {
		t.Fatal("expected malformed ACL file error")
	}

	if _, ok := acl.GetUser("existing"); !ok {
		t.Fatal(
			"active ACL changed after failed LOAD",
		)
	}

	if _, ok := acl.GetUser("broken"); ok {
		t.Fatal(
			"partially loaded user became active",
		)
	}
}

func TestACLFileAcceptsHashedPassword(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.acl")

	hash := aclPasswordHash("secret")

	data := []byte(
		"user default on nopass ~* &* +@all\n" +
			"user hashed on #" + hash +
			" ~* &* -@all +get\n",
	)

	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	acl := NewACL()

	if err := acl.LoadFile(path); err != nil {
		t.Fatal(err)
	}

	if !acl.Authenticate("hashed", "secret") {
		t.Fatal("hashed password was not restored")
	}
}

func TestACLFileStartupLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.acl")

	hash := aclPasswordHash("startup-pass")

	data := []byte(
		"user default on nopass sanitize-payload ~* &* +@all\n" +
			"user startup on sanitize-payload #" + hash +
			" ~startup:* &* -@all +@read +set\n",
	)

	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	store := engine.New()

	cfg := config.Default()
	cfg.ListenAddr = "127.0.0.1:0"
	cfg.AdminAddr = ""
	cfg.ACLFile = path

	server, err := ListenWithConfig(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	user, ok := server.server.acl.GetUser("startup")
	if !ok {
		t.Fatal("startup ACL user was not loaded")
	}

	if !user.Enabled {
		t.Fatal("startup ACL user is disabled")
	}

	if !server.server.acl.Authenticate(
		"startup",
		"startup-pass",
	) {
		t.Fatal("startup password was not restored")
	}

	if !server.server.acl.CommandAllowed(
		"startup",
		"get",
	) {
		t.Fatal("startup +@read rule missing")
	}

	if !server.server.acl.CommandAllowed(
		"startup",
		"set",
	) {
		t.Fatal("startup +set rule missing")
	}

	if server.server.acl.CommandAllowed(
		"startup",
		"del",
	) {
		t.Fatal("unexpected startup DEL permission")
	}

	if !server.server.acl.KeyAllowed(
		"startup",
		"startup:key",
	) {
		t.Fatal("startup key pattern missing")
	}
}

func TestACLFileStartupRejectsMalformedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.acl")

	data := []byte(
		"user default on nopass ~* &* +@all\n" +
			"user broken on ~* +@does-not-exist\n",
	)

	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	store := engine.New()

	cfg := config.Default()
	cfg.ListenAddr = "127.0.0.1:0"
	cfg.AdminAddr = ""
	cfg.ACLFile = path

	server, err := ListenWithConfig(cfg, store)

	if server != nil {
		_ = server.Close()
		t.Fatal("server started with malformed ACL file")
	}

	if err == nil {
		t.Fatal("expected startup ACL error")
	}

	if !strings.Contains(
		err.Error(),
		"no change to the previously active ACL rules was performed",
	) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestACLFileStartupRejectsMissingFile(t *testing.T) {
	dir := t.TempDir()

	store := engine.New()

	cfg := config.Default()
	cfg.ListenAddr = "127.0.0.1:0"
	cfg.AdminAddr = ""
	cfg.ACLFile = filepath.Join(
		dir,
		"missing-users.acl",
	)

	server, err := ListenWithConfig(cfg, store)

	if server != nil {
		_ = server.Close()
		t.Fatal("server started with missing ACL file")
	}

	if err == nil {
		t.Fatal("expected missing ACL file error")
	}
}
