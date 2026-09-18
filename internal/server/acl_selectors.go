package server

import (
	"fmt"
	"strings"
)

type ACLSelector struct {
	AllCommands  bool
	CommandRules []string
	CommandAllow map[string]bool

	AllKeys     bool
	KeyPatterns []string

	AllChannels     bool
	ChannelPatterns []string
}

func newACLSelector() ACLSelector {
	return ACLSelector{
		AllCommands:  false,
		CommandRules: []string{"-@all"},
		CommandAllow: make(map[string]bool),
	}
}

func cloneACLSelector(in ACLSelector) ACLSelector {
	out := in

	out.CommandRules = append(
		[]string(nil),
		in.CommandRules...,
	)

	out.KeyPatterns = append(
		[]string(nil),
		in.KeyPatterns...,
	)

	out.ChannelPatterns = append(
		[]string(nil),
		in.ChannelPatterns...,
	)

	out.CommandAllow = make(
		map[string]bool,
		len(in.CommandAllow),
	)

	for command, allowed := range in.CommandAllow {
		out.CommandAllow[command] = allowed
	}

	return out
}

func parseACLSelector(raw string) (ACLSelector, error) {
	selector := newACLSelector()

	if !strings.HasPrefix(raw, "(") {
		return selector, fmt.Errorf(
			"ERR Error in ACL SETUSER modifier '%s': Syntax error",
			raw,
		)
	}

	if !strings.HasSuffix(raw, ")") {
		return selector, fmt.Errorf(
			"ERR Unmatched parenthesis in acl selector starting at '%s'.",
			raw,
		)
	}

	inside := raw[1 : len(raw)-1]

	// Redis does not permit nested selectors.
	if strings.Contains(inside, "(") ||
		strings.Contains(inside, ")") {
		return selector, fmt.Errorf(
			"ERR Error in ACL SETUSER modifier '%s': Syntax error",
			raw,
		)
	}

	// () and (   ) are both valid.
	for _, rule := range strings.Fields(inside) {
		if err := applyACLSelectorRule(
			&selector,
			rule,
		); err != nil {
			// User-state/password/payload modifiers are illegal
			// inside selectors.
			return selector, fmt.Errorf(
				"ERR Error in ACL SETUSER modifier '%s': Syntax error",
				raw,
			)
		}
	}

	return selector, nil
}

func applyACLSelectorRule(
	s *ACLSelector,
	rule string,
) error {
	lower := strings.ToLower(rule)

	switch {
	case lower == "allkeys":
		s.AllKeys = true
		s.KeyPatterns = []string{"*"}

	case lower == "resetkeys":
		s.AllKeys = false
		s.KeyPatterns = nil

	case strings.HasPrefix(rule, "~"):
		pattern := strings.TrimPrefix(rule, "~")

		s.AllKeys = pattern == "*"

		if pattern == "*" {
			s.KeyPatterns = []string{"*"}
		} else {
			s.KeyPatterns = append(
				s.KeyPatterns,
				pattern,
			)
		}

	case lower == "allchannels":
		s.AllChannels = true
		s.ChannelPatterns = []string{"*"}

	case lower == "resetchannels":
		s.AllChannels = false
		s.ChannelPatterns = nil

	case strings.HasPrefix(rule, "&"):
		pattern := strings.TrimPrefix(rule, "&")

		if pattern == "*" {
			s.AllChannels = true
			s.ChannelPatterns = []string{"*"}
			return nil
		}

		if !s.AllChannels {
			s.ChannelPatterns = append(
				s.ChannelPatterns,
				pattern,
			)
		}

	case lower == "allcommands" ||
		lower == "+@all":
		s.AllCommands = true
		s.CommandAllow = make(map[string]bool)
		s.CommandRules = []string{"+@all"}

	case lower == "nocommands" ||
		lower == "-@all":
		s.AllCommands = false
		s.CommandAllow = make(map[string]bool)
		s.CommandRules = []string{"-@all"}

	case strings.HasPrefix(lower, "+@"):
		category := strings.TrimPrefix(lower, "+@")

		commands, ok := aclCommandsForCategory(category)
		if !ok {
			return fmt.Errorf("unknown category")
		}

		for _, command := range commands {
			s.CommandAllow[command] = true
		}

		s.CommandRules = append(
			s.CommandRules,
			"+@"+category,
		)

	case strings.HasPrefix(lower, "-@"):
		category := strings.TrimPrefix(lower, "-@")

		commands, ok := aclCommandsForCategory(category)
		if !ok {
			return fmt.Errorf("unknown category")
		}

		for _, command := range commands {
			s.CommandAllow[command] = false
		}

		s.CommandRules = append(
			s.CommandRules,
			"-@"+category,
		)

	case strings.HasPrefix(rule, "+"):
		command := strings.ToLower(
			strings.TrimPrefix(rule, "+"),
		)

		if command == "" {
			return fmt.Errorf("invalid command")
		}

		s.CommandAllow[command] = true
		s.CommandRules = append(
			s.CommandRules,
			"+"+command,
		)

	case strings.HasPrefix(rule, "-"):
		command := strings.ToLower(
			strings.TrimPrefix(rule, "-"),
		)

		if command == "" {
			return fmt.Errorf("invalid command")
		}

		s.CommandAllow[command] = false
		s.CommandRules = append(
			s.CommandRules,
			"-"+command,
		)

	default:
		return fmt.Errorf("invalid selector modifier")
	}

	return nil
}

func aclSelectorKeysString(
	s ACLSelector,
) string {
	var out strings.Builder

	for _, pattern := range s.KeyPatterns {
		if out.Len() > 0 {
			out.WriteByte(' ')
		}

		out.WriteByte('~')
		out.WriteString(pattern)
	}

	return out.String()
}

func aclSelectorChannelsString(
	s ACLSelector,
) string {
	var out strings.Builder

	for _, pattern := range s.ChannelPatterns {
		if out.Len() > 0 {
			out.WriteByte(' ')
		}

		out.WriteByte('&')
		out.WriteString(pattern)
	}

	return out.String()
}

func aclSelectorListFragment(
	s ACLSelector,
) string {
	var out strings.Builder

	out.WriteByte('(')

	first := true

	for _, pattern := range s.KeyPatterns {
		if !first {
			out.WriteByte(' ')
		}
		first = false

		out.WriteByte('~')
		out.WriteString(pattern)
	}

	if s.AllChannels {
		if !first {
			out.WriteByte(' ')
		}
		first = false
		out.WriteString("&*")
	} else {
		if !first {
			out.WriteByte(' ')
		}
		first = false
		out.WriteString("resetchannels")

		for _, pattern := range s.ChannelPatterns {
			out.WriteString(" &")
			out.WriteString(pattern)
		}
	}

	for _, rule := range s.CommandRules {
		if !first {
			out.WriteByte(' ')
		}
		first = false

		out.WriteString(rule)
	}

	out.WriteByte(')')

	return out.String()
}

func aclRuleCommandAllowed(
	allCommands bool,
	commandAllow map[string]bool,
	command string,
) bool {
	command = strings.ToLower(command)

	if allowed, exists := commandAllow[command]; exists {
		return allowed
	}

	return allCommands
}

func aclRuleKeyAllowed(
	allKeys bool,
	patterns []string,
	key string,
) bool {
	if allKeys {
		return true
	}

	for _, pattern := range patterns {
		if aclGlobMatch(pattern, key) {
			return true
		}
	}

	return false
}

func aclRuleChannelAllowed(
	allChannels bool,
	patterns []string,
	channel string,
) bool {
	if allChannels {
		return true
	}

	for _, pattern := range patterns {
		if aclGlobMatch(pattern, channel) {
			return true
		}
	}

	return false
}

func aclRuleChannelPatternAllowed(
	allChannels bool,
	patterns []string,
	pattern string,
) bool {
	if allChannels {
		return true
	}

	for _, allowed := range patterns {
		if allowed == pattern {
			return true
		}
	}

	return false
}

func aclRuleSetAllows(
	command string,
	args [][]byte,

	allCommands bool,
	commandAllow map[string]bool,

	allKeys bool,
	keyPatterns []string,

	allChannels bool,
	channelPatterns []string,
) bool {
	if !aclRuleCommandAllowed(
		allCommands,
		commandAllow,
		command,
	) {
		return false
	}

	refs, err := commandKeys(args)
	if err == nil {
		for _, ref := range refs {
			if !aclRuleKeyAllowed(
				allKeys,
				keyPatterns,
				string(ref.value),
			) {
				return false
			}
		}
	}

	if len(args) < 2 {
		return true
	}

	switch strings.ToUpper(string(args[0])) {
	case "PUBLISH", "SPUBLISH":
		return aclRuleChannelAllowed(
			allChannels,
			channelPatterns,
			string(args[1]),
		)

	case "SUBSCRIBE", "SSUBSCRIBE":
		for _, raw := range args[1:] {
			if !aclRuleChannelAllowed(
				allChannels,
				channelPatterns,
				string(raw),
			) {
				return false
			}
		}

	case "PSUBSCRIBE":
		for _, raw := range args[1:] {
			if !aclRuleChannelPatternAllowed(
				allChannels,
				channelPatterns,
				string(raw),
			) {
				return false
			}
		}
	}

	return true
}

func aclUserAllowsCommand(
	user *ACLUser,
	args [][]byte,
) bool {
	if user == nil || len(args) == 0 {
		return false
	}

	command := aclCanonicalCommand(args)

	// Root rule set.
	if aclRuleSetAllows(
		command,
		args,

		user.AllCommands,
		user.CommandAllow,

		user.AllKeys,
		user.KeyPatterns,

		user.AllChannels,
		user.ChannelPatterns,
	) {
		return true
	}

	// Any complete selector may independently grant access.
	for _, selector := range user.Selectors {
		if aclRuleSetAllows(
			command,
			args,

			selector.AllCommands,
			selector.CommandAllow,

			selector.AllKeys,
			selector.KeyPatterns,

			selector.AllChannels,
			selector.ChannelPatterns,
		) {
			return true
		}
	}

	return false
}
