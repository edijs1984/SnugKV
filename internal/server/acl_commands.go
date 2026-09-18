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

	// AUTH must remain available before authentication succeeds.
	if strings.EqualFold(
		string(args[0]),
		"AUTH",
	) {
		return nil
	}

	username := "default"

	if session != nil && session.username != "" {
		username = session.username
	}

	user, ok := s.acl.GetUser(username)
	if !ok || !user.Enabled {
		return errors.New(
			"NOAUTH Authentication required.",
		)
	}

	if aclUserAllowsCommand(user, args) {
		return nil
	}

	canonical := aclCanonicalCommand(args)

	// Preserve Redis's reason ordering:
	// command denial first, then key, then channel.
	commandPossible := aclDryRunCommandAllowed(
		user,
		canonical,
	)

	if !commandPossible {
		for _, selector := range user.Selectors {
			if aclRuleCommandAllowed(
				selector.AllCommands,
				selector.CommandAllow,
				canonical,
			) {
				commandPossible = true
				break
			}
		}
	}

	if !commandPossible {
		return fmt.Errorf(
			"NOPERM User %s has no permissions to run the '%s' command",
			username,
			canonical,
		)
	}

	refs, err := commandKeys(args)
	if err == nil && len(refs) > 0 {
		keyPossible := true

		for _, ref := range refs {
			key := string(ref.value)
			matched := aclDryRunKeyAllowed(
				user,
				key,
			)

			if !matched {
				for _, selector := range user.Selectors {
					if aclRuleKeyAllowed(
						selector.AllKeys,
						selector.KeyPatterns,
						key,
					) {
						matched = true
						break
					}
				}
			}

			if !matched {
				keyPossible = false
				break
			}
		}

		if !keyPossible {
			return errors.New(
				"NOPERM No permissions to access a key",
			)
		}
	}

	if denied := firstDeniedACLChannelAcrossUser(
		user,
		args,
	); denied != "" {
		return errors.New(
			"NOPERM No permissions to access a channel",
		)
	}

	// At this point individual dimensions may each be allowed by
	// different rule sets, but no single complete root/selector rule
	// set matched. Redis still reports a key/channel-style denial
	// depending on the command surface. Prefer key when keys exist.
	if err == nil && len(refs) > 0 {
		return errors.New(
			"NOPERM No permissions to access a key",
		)
	}

	return errors.New(
		"NOPERM No permissions to access a channel",
	)
}

func firstDeniedACLChannelAcrossUser(
	user *ACLUser,
	args [][]byte,
) string {
	if len(args) < 2 {
		return ""
	}

	command := strings.ToUpper(string(args[0]))

	channelAllowed := func(channel string) bool {
		if aclRuleChannelAllowed(
			user.AllChannels,
			user.ChannelPatterns,
			channel,
		) {
			return true
		}

		for _, selector := range user.Selectors {
			if aclRuleChannelAllowed(
				selector.AllChannels,
				selector.ChannelPatterns,
				channel,
			) {
				return true
			}
		}

		return false
	}

	patternAllowed := func(pattern string) bool {
		if aclRuleChannelPatternAllowed(
			user.AllChannels,
			user.ChannelPatterns,
			pattern,
		) {
			return true
		}

		for _, selector := range user.Selectors {
			if aclRuleChannelPatternAllowed(
				selector.AllChannels,
				selector.ChannelPatterns,
				pattern,
			) {
				return true
			}
		}

		return false
	}

	switch command {
	case "PUBLISH", "SPUBLISH":
		channel := string(args[1])

		if !channelAllowed(channel) {
			return channel
		}

	case "SUBSCRIBE", "SSUBSCRIBE":
		for _, raw := range args[1:] {
			channel := string(raw)

			if !channelAllowed(channel) {
				return channel
			}
		}

	case "PSUBSCRIBE":
		for _, raw := range args[1:] {
			pattern := string(raw)

			if !patternAllowed(pattern) {
				return pattern
			}
		}
	}

	return ""
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

func (s *Server) authorizeCommandChannels(
	username string,
	args [][]byte,
) error {
	if len(args) < 2 {
		return nil
	}

	command := strings.ToUpper(string(args[0]))

	switch command {
	case "PUBLISH", "SPUBLISH":
		if !s.acl.ChannelAllowed(
			username,
			string(args[1]),
		) {
			return errors.New(
				"NOPERM No permissions to access a channel",
			)
		}

	case "SUBSCRIBE", "SSUBSCRIBE":
		for _, raw := range args[1:] {
			if !s.acl.ChannelAllowed(
				username,
				string(raw),
			) {
				return errors.New(
					"NOPERM No permissions to access a channel",
				)
			}
		}

	case "PSUBSCRIBE":
		for _, raw := range args[1:] {
			if !s.acl.ChannelPatternAllowed(
				username,
				string(raw),
			) {
				return errors.New(
					"NOPERM No permissions to access a channel",
				)
			}
		}
	}

	return nil
}

func (s *Server) firstDeniedACLChannel(
	username string,
	args [][]byte,
) string {
	if len(args) < 2 {
		return ""
	}

	command := strings.ToUpper(string(args[0]))

	switch command {
	case "PUBLISH", "SPUBLISH":
		if !s.acl.ChannelAllowed(
			username,
			string(args[1]),
		) {
			return string(args[1])
		}

	case "SUBSCRIBE", "SSUBSCRIBE":
		for _, raw := range args[1:] {
			if !s.acl.ChannelAllowed(
				username,
				string(raw),
			) {
				return string(raw)
			}
		}

	case "PSUBSCRIBE":
		for _, raw := range args[1:] {
			if !s.acl.ChannelPatternAllowed(
				username,
				string(raw),
			) {
				return string(raw)
			}
		}
	}

	return ""
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

	case "SAVE":
		if len(args) != 2 {
			return nil, errors.New(
				"ERR wrong number of arguments for 'acl|save' command",
			)
		}

		if s.configACLFile == "" {
			return nil, errors.New(
				"ERR This Redis instance is not configured to use an ACL file. You may want to specify users via the ACL SETUSER command and then issue a CONFIG REWRITE (assuming you have a Redis configuration file set) in order to store users in the Redis configuration.",
			)
		}

		if err := s.acl.SaveFile(s.configACLFile); err != nil {
			return nil, fmt.Errorf(
				"ERR %s",
				err.Error(),
			)
		}

		return []byte("+OK\r\n"), nil

	case "LOAD":
		if len(args) != 2 {
			return nil, errors.New(
				"ERR wrong number of arguments for 'acl|load' command",
			)
		}

		if s.configACLFile == "" {
			return nil, errors.New(
				"ERR This Redis instance is not configured to use an ACL file. You may want to specify users via the ACL SETUSER command and then issue a CONFIG REWRITE (assuming you have a Redis configuration file set) in order to store users in the Redis configuration.",
			)
		}

		if err := s.acl.LoadFile(s.configACLFile); err != nil {
			return nil, err
		}

		return []byte("+OK\r\n"), nil

	case "LOG":
		if len(args) > 3 {
			return nil, errors.New(
				"ERR unknown subcommand or wrong number of arguments for 'LOG'. Try ACL HELP.",
			)
		}

		if len(args) == 3 &&
			strings.EqualFold(string(args[2]), "RESET") {
			s.aclLog.Reset()
			return []byte("+OK\r\n"), nil
		}

		limit := int64(10)

		if len(args) == 3 {
			parsed, err := strconv.ParseInt(
				string(args[2]),
				10,
				64,
			)
			if err != nil {
				return nil, errors.New(
					"ERR value is not an integer or out of range",
				)
			}

			limit = parsed
		}

		if limit <= 0 {
			return array(), nil
		}

		return aclLogReply(s.aclLog.Entries(limit)), nil

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

	if user.SanitizePayload {
		flags = append(
			flags,
			formatBulkString([]byte("sanitize-payload")),
		)
	} else {
		flags = append(
			flags,
			formatBulkString([]byte("skip-sanitize-payload")),
		)
	}

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

	channelsReply := []byte{}

	for _, pattern := range user.ChannelPatterns {
		if len(channelsReply) > 0 {
			channelsReply = append(
				channelsReply,
				' ',
			)
		}

		channelsReply = append(
			channelsReply,
			'&',
		)

		channelsReply = append(
			channelsReply,
			pattern...,
		)
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
		formatBulkString(channelsReply),

		formatBulkString([]byte("selectors")),
		aclSelectorsReply(user.Selectors),
	)
}

func aclSelectorsReply(
	selectors []ACLSelector,
) []byte {
	items := make([][]byte, 0, len(selectors))

	for _, selector := range selectors {
		commands := strings.Join(
			selector.CommandRules,
			" ",
		)

		keys := aclSelectorKeysString(selector)
		channels := aclSelectorChannelsString(selector)

		items = append(
			items,
			array(
				formatBulkString([]byte("commands")),
				formatBulkString([]byte(commands)),

				formatBulkString([]byte("keys")),
				formatBulkString([]byte(keys)),

				formatBulkString([]byte("channels")),
				formatBulkString([]byte(channels)),
			),
		)
	}

	return array(items...)
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

	if user.SanitizePayload {
		out.WriteString(" sanitize-payload")
	} else {
		out.WriteString(" skip-sanitize-payload")
	}

	for _, hash := range user.PasswordHashes {
		out.WriteString(" #")
		out.WriteString(hash)
	}

	for _, pattern := range user.KeyPatterns {
		out.WriteString(" ~")
		out.WriteString(pattern)
	}

	if user.AllChannels {
		out.WriteString(" &*")
	} else {
		out.WriteString(" resetchannels")

		for _, pattern := range user.ChannelPatterns {
			out.WriteString(" &")
			out.WriteString(pattern)
		}
	}

	for _, rule := range user.CommandRules {
		out.WriteByte(' ')
		out.WriteString(rule)
	}

	for _, selector := range user.Selectors {
		out.WriteByte(' ')
		out.WriteString(
			aclSelectorListFragment(selector),
		)
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

func aclDryRunChannelAllowed(
	user *ACLUser,
	channel string,
) bool {
	if user.AllChannels {
		return true
	}

	for _, pattern := range user.ChannelPatterns {
		if aclGlobMatch(pattern, channel) {
			return true
		}
	}

	return false
}

func aclDryRunChannelPatternAllowed(
	user *ACLUser,
	pattern string,
) bool {
	if user.AllChannels {
		return true
	}

	for _, allowed := range user.ChannelPatterns {
		if allowed == pattern {
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

	if aclUserAllowsCommand(user, commandArgs) {
		return []byte("+OK\r\n"), nil
	}

	commandPossible := aclDryRunCommandAllowed(
		user,
		canonical,
	)

	if !commandPossible {
		for _, selector := range user.Selectors {
			if aclRuleCommandAllowed(
				selector.AllCommands,
				selector.CommandAllow,
				canonical,
			) {
				commandPossible = true
				break
			}
		}
	}

	if !commandPossible {
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

			keyPossible := aclDryRunKeyAllowed(
				user,
				key,
			)

			if !keyPossible {
				for _, selector := range user.Selectors {
					if aclRuleKeyAllowed(
						selector.AllKeys,
						selector.KeyPatterns,
						key,
					) {
						keyPossible = true
						break
					}
				}
			}

			if !keyPossible {
				return formatBulkString([]byte(fmt.Sprintf(
					"User %s has no permissions to access the '%s' key",
					username,
					key,
				))), nil
			}
		}
	}

	if denied := firstDeniedACLChannelAcrossUser(
		user,
		commandArgs,
	); denied != "" {
		return formatBulkString([]byte(fmt.Sprintf(
			"User %s has no permissions to access the '%s' channel",
			username,
			denied,
		))), nil
	}

	// Dimensions may individually be covered by different selectors,
	// but no single selector matched the whole command.
	if err == nil && len(refs) > 0 {
		key := string(refs[0].value)

		return formatBulkString([]byte(fmt.Sprintf(
			"User %s has no permissions to access the '%s' key",
			username,
			key,
		))), nil
	}

	return formatBulkString([]byte(fmt.Sprintf(
		"User %s has no permissions to run the '%s' command",
		username,
		canonical,
	))), nil
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

func (s *Server) firstDeniedACLKey(
	username string,
	args [][]byte,
) string {
	refs, err := commandKeys(args)
	if err != nil {
		return ""
	}

	for _, ref := range refs {
		key := string(ref.value)

		if !s.acl.KeyAllowed(username, key) {
			return key
		}
	}

	return ""
}
