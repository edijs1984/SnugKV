package server

import (
	"strings"
	"testing"
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
