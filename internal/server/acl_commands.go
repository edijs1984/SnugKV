package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type authSession struct {
	username      string
	authenticated bool
}

func newAuthSession(acl *ACL) *authSession {
	return &authSession{
		username:      "default",
		authenticated: acl.DefaultUserNoPass(),
	}
}

func aclCanonicalCommand(args [][]byte) string {
	if len(args) == 0 {
		return ""
	}

	cmd := strings.ToLower(string(args[0]))

	if cmd == "acl" && len(args) > 1 {
		return "acl|" + strings.ToLower(string(args[1]))
	}

	return cmd
}

func (s *Server) executeAUTH(
	session *authSession,
	args [][]byte,
) ([]byte, error) {
	switch len(args) {
	case 1:
		return nil, errors.New(
			"ERR wrong number of arguments for 'auth' command",
		)

	case 2:
		if s.acl.DefaultUserNoPass() {
			return nil, errors.New(
				"ERR AUTH <password> called without any password configured for the default user. Are you sure your configuration is correct?",
			)
		}

		if !s.acl.Authenticate("default", string(args[1])) {
			return nil, errors.New(
				"WRONGPASS invalid username-password pair or user is disabled.",
			)
		}

		session.username = "default"
		session.authenticated = true

		return []byte("+OK\r\n"), nil

	case 3:
		username := string(args[1])
		password := string(args[2])

		if !s.acl.Authenticate(username, password) {
			return nil, errors.New(
				"WRONGPASS invalid username-password pair or user is disabled.",
			)
		}

		session.username = username
		session.authenticated = true

		return []byte("+OK\r\n"), nil

	default:
		return nil, errors.New("ERR syntax error")
	}
}

func (s *Server) authorizeConnectionCommand(
	session *authSession,
	args [][]byte,
) error {
	if len(args) == 0 {
		return nil
	}

	cmd := strings.ToUpper(string(args[0]))

	// AUTH must always remain available to unauthenticated connections.
	if cmd == "AUTH" {
		return nil
	}

	if !session.authenticated {
		return errors.New("NOAUTH Authentication required.")
	}

	// Re-check user state on every command so disabling/deleting a user
	// immediately revokes existing sessions.
	user, ok := s.acl.GetUser(session.username)
	if !ok || !user.Enabled {
		session.authenticated = false
		return errors.New("NOAUTH Authentication required.")
	}

	canonical := aclCanonicalCommand(args)

	if !s.acl.CommandAllowed(session.username, canonical) {
		return fmt.Errorf(
			"NOPERM User %s has no permissions to run the '%s' command",
			session.username,
			canonical,
		)
	}

	if err := s.authorizeCommandKeys(session.username, args); err != nil {
		return err
	}

	return nil
}

func (s *Server) authorizeCommandKeys(
	username string,
	args [][]byte,
) error {
	refs, err := commandKeys(args)
	if err != nil {
		// Preserve the normal command parser's Redis-compatible syntax/arity
		// error rather than replacing it with a COMMAND GETKEYS-style error.
		//
		// A malformed command cannot execute anyway, so there is no ACL
		// bypass from deferring this error to the normal command handler.
		return nil
	}

	for _, ref := range refs {
		if !s.acl.KeyAllowed(username, string(ref.value)) {
			return errors.New("NOPERM No permissions to access a key")
		}
	}

	return nil
}

func (s *Server) executeACL(
	session *authSession,
	args [][]byte,
) ([]byte, error) {
	if len(args) < 2 {
		return nil, errors.New(
			"ERR wrong number of arguments for 'acl' command",
		)
	}

	sub := strings.ToUpper(string(args[1]))

	switch sub {
	case "WHOAMI":
		if len(args) != 2 {
			return nil, errors.New(
				"ERR wrong number of arguments for 'acl|whoami' command",
			)
		}

		return formatBulkString([]byte(session.username)), nil

	case "USERS":
		if len(args) != 2 {
			return nil, errors.New(
				"ERR wrong number of arguments for 'acl|users' command",
			)
		}

		names := s.acl.Users()
		items := make([][]byte, 0, len(names))

		for _, name := range names {
			items = append(items, formatBulkString([]byte(name)))
		}

		return array(items...), nil

	case "GETUSER":
		if len(args) != 3 {
			return nil, errors.New(
				"ERR wrong number of arguments for 'acl|getuser' command",
			)
		}

		user, ok := s.acl.GetUser(string(args[2]))
		if !ok {
			return nullBulk(), nil
		}

		return aclGetUserReply(user), nil

	case "LIST":
		if len(args) != 2 {
			return nil, errors.New(
				"ERR wrong number of arguments for 'acl|list' command",
			)
		}

		names := s.acl.Users()
		items := make([][]byte, 0, len(names))

		for _, name := range names {
			user, ok := s.acl.GetUser(name)
			if !ok {
				continue
			}

			items = append(
				items,
				formatBulkString([]byte(aclUserListLine(user))),
			)
		}

		return array(items...), nil

	case "SETUSER":
		if len(args) < 3 {
			return nil, errors.New(
				"ERR wrong number of arguments for 'acl|setuser' command",
			)
		}

		rules := make([]string, 0, len(args)-3)

		for _, raw := range args[3:] {
			rules = append(rules, string(raw))
		}

		if err := s.acl.SetUser(string(args[2]), rules); err != nil {
			return nil, err
		}

		return []byte("+OK\r\n"), nil

	case "DELUSER":
		if len(args) < 3 {
			return nil, errors.New(
				"ERR wrong number of arguments for 'acl|deluser' command",
			)
		}

		names := make([]string, 0, len(args)-2)

		for _, raw := range args[2:] {
			names = append(names, string(raw))
		}

		return integer(s.acl.DeleteUsers(names...)), nil

	case "CAT":
		if len(args) > 3 {
			return nil, errors.New(
				"ERR unknown subcommand or wrong number of arguments for 'CAT'. Try ACL HELP.",
			)
		}

		category := ""
		if len(args) == 3 {
			category = string(args[2])
		}

		return aclCategoryReply(category)

	case "DRYRUN":
		return s.executeACLDryRun(args)

	case "GENPASS":
		return executeACLGenPass(args)

	case "HELP":
		if len(args) != 2 {
			return nil, errors.New(
				"ERR wrong number of arguments for 'acl|help' command",
			)
		}

		return aclHelpReply(), nil

	default:
		return nil, fmt.Errorf(
			"ERR unknown subcommand '%s'. Try ACL HELP.",
			sub,
		)
	}
}

func aclGetUserReply(user *ACLUser) []byte {
	flags := make([][]byte, 0, 3)

	if user.Enabled {
		flags = append(flags, formatBulkString([]byte("on")))
	} else {
		flags = append(flags, formatBulkString([]byte("off")))
	}

	if user.NoPass {
		flags = append(flags, formatBulkString([]byte("nopass")))
	}

	flags = append(
		flags,
		formatBulkString([]byte("sanitize-payload")),
	)

	passwords := make([][]byte, 0, len(user.PasswordHashes))
	for _, hash := range user.PasswordHashes {
		passwords = append(passwords, formatBulkString([]byte(hash)))
	}

	keysReply := []byte{}
	for _, pattern := range user.KeyPatterns {
		if len(keysReply) > 0 {
			keysReply = append(keysReply, ' ')
		}

		keysReply = append(keysReply, '~')
		keysReply = append(keysReply, pattern...)
	}

	commands := strings.Join(user.CommandRules, " ")

	channels := ""
	if user.Name == "default" {
		channels = "&*"
	}

	return array(
		formatBulkString([]byte("flags")),
		array(flags...),

		formatBulkString([]byte("passwords")),
		array(passwords...),

		formatBulkString([]byte("commands")),
		formatBulkString([]byte(commands)),

		formatBulkString([]byte("keys")),
		formatBulkString(keysReply),

		formatBulkString([]byte("channels")),
		formatBulkString([]byte(channels)),

		formatBulkString([]byte("selectors")),
		array(),
	)
}

func aclUserListLine(user *ACLUser) string {
	var out strings.Builder

	out.WriteString("user ")
	out.WriteString(user.Name)

	if user.Enabled {
		out.WriteString(" on")
	} else {
		out.WriteString(" off")
	}

	if user.NoPass {
		out.WriteString(" nopass")
	}

	out.WriteString(" sanitize-payload")

	for _, hash := range user.PasswordHashes {
		out.WriteString(" #")
		out.WriteString(hash)
	}

	for _, pattern := range user.KeyPatterns {
		out.WriteString(" ~")
		out.WriteString(pattern)
	}

	if user.Name == "default" {
		out.WriteString(" &*")
	} else {
		out.WriteString(" resetchannels")
	}

	for _, rule := range user.CommandRules {
		out.WriteByte(' ')
		out.WriteString(rule)
	}

	return out.String()
}

func aclHelpReply() []byte {
	lines := []string{
		"ACL <subcommand> [<arg> [value] [opt] ...]. Subcommands are:",
		"CAT [<category>]",
		"    List all commands that belong to <category>, or all command categories",
		"    when no category is specified.",
		"DELUSER <username> [<username> ...]",
		"    Delete a list of users.",
		"DRYRUN <username> <command> [<arg> ...]",
		"    Returns whether the user can execute the given command without executing the command.",
		"GETUSER <username>",
		"    Get the user's details.",
		"GENPASS [<bits>]",
		"    Generate a secure 256-bit user password. The optional `bits` argument can",
		"    be used to specify a different size.",
		"LIST",
		"    Show users details in config file format.",
		"LOAD",
		"    Reload users from the ACL file.",
		"LOG [<count> | RESET]",
		"    Show the ACL log entries.",
		"SAVE",
		"    Save the current config to the ACL file.",
		"SETUSER <username> <attribute> [<attribute> ...]",
		"    Create or modify a user with the specified attributes.",
		"USERS",
		"    List all the registered usernames.",
		"WHOAMI",
		"    Return the current connection username.",
		"HELP",
		"    Print this help.",
	}

	items := make([][]byte, 0, len(lines))

	for _, line := range lines {
		items = append(items, formatBulkString([]byte(line)))
	}

	return array(items...)
}

func aclDryRunCommandAllowed(user *ACLUser, command string) bool {
	command = strings.ToLower(command)

	if allowed, exists := user.CommandAllow[command]; exists {
		return allowed
	}

	return user.AllCommands
}

func aclDryRunKeyAllowed(user *ACLUser, key string) bool {
	if user.AllKeys {
		return true
	}

	for _, pattern := range user.KeyPatterns {
		if aclGlobMatch(pattern, key) {
			return true
		}
	}

	return false
}

func (s *Server) executeACLDryRun(args [][]byte) ([]byte, error) {
	// ACL DRYRUN <username> <command> [<arg> ...]
	if len(args) < 4 {
		return nil, errors.New(
			"ERR wrong number of arguments for 'acl|dryrun' command",
		)
	}

	username := string(args[2])

	user, ok := s.acl.GetUser(username)
	if !ok {
		return nil, fmt.Errorf(
			"ERR User '%s' not found",
			username,
		)
	}

	commandArgs := args[3:]

	if len(commandArgs) == 0 {
		return nil, errors.New(
			"ERR wrong number of arguments for 'acl|dryrun' command",
		)
	}

	commandName := strings.ToUpper(string(commandArgs[0]))

	info, exists := commandTable[commandName]
	if !exists {
		return nil, fmt.Errorf(
			"ERR Command '%s' not found",
			commandName,
		)
	}

	if len(commandArgs) < info.min ||
		(info.max > 0 && len(commandArgs) > info.max) {
		return nil, fmt.Errorf(
			"ERR wrong number of arguments for '%s' command",
			strings.ToLower(commandName),
		)
	}

	canonical := aclCanonicalCommand(commandArgs)

	// Redis DRYRUN evaluates the user's ACL rules even when that user is
	// currently disabled. It does not perform authentication-state checks.
	if !aclDryRunCommandAllowed(user, canonical) {
		return formatBulkString([]byte(fmt.Sprintf(
			"User %s has no permissions to run the '%s' command",
			username,
			canonical,
		))), nil
	}

	refs, err := commandKeys(commandArgs)
	if err == nil {
		for _, ref := range refs {
			key := string(ref.value)

			if !aclDryRunKeyAllowed(user, key) {
				return formatBulkString([]byte(fmt.Sprintf(
					"User %s has no permissions to access the '%s' key",
					username,
					key,
				))), nil
			}
		}
	}

	return []byte("+OK\r\n"), nil
}

func executeACLGenPass(args [][]byte) ([]byte, error) {
	if len(args) > 3 {
		return nil, errors.New(
			"ERR unknown subcommand or wrong number of arguments for 'GENPASS'. Try ACL HELP.",
		)
	}

	bits := 256

	if len(args) == 3 {
		parsed, err := strconv.Atoi(string(args[2]))
		if err != nil {
			return nil, errors.New(
				"ERR value is not an integer or out of range",
			)
		}

		bits = parsed
	}

	if bits <= 0 || bits > 4096 {
		return nil, errors.New(
			"ERR ACL GENPASS argument must be the number of bits for the output password, a positive number up to 4096",
		)
	}

	// Redis exposes generated passwords as hexadecimal. Allocate enough
	// random bytes to provide at least the requested number of bits, then
	// truncate the textual representation to ceil(bits / 4) hex digits.
	byteCount := (bits + 7) / 8
	hexChars := (bits + 3) / 4

	random := make([]byte, byteCount)

	if _, err := rand.Read(random); err != nil {
		return nil, errors.New("ERR secure random generation failed")
	}

	encoded := hex.EncodeToString(random)

	if len(encoded) > hexChars {
		encoded = encoded[:hexChars]
	}

	return formatBulkString([]byte(encoded)), nil
}
