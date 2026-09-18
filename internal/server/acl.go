package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

type ACLUser struct {
	Name string

	Enabled bool
	NoPass  bool

	PasswordHashes []string

	AllCommands  bool
	CommandRules []string
	CommandAllow map[string]bool

	AllKeys     bool
	KeyPatterns []string

	AllChannels     bool
	ChannelPatterns []string

	SanitizePayload bool

	Selectors []ACLSelector
}

type ACL struct {
	mu    sync.RWMutex
	users map[string]*ACLUser
}

func NewACL() *ACL {
	a := &ACL{
		users: make(map[string]*ACLUser),
	}

	a.users["default"] = &ACLUser{
		Name:            "default",
		Enabled:         true,
		NoPass:          true,
		PasswordHashes:  nil,
		AllCommands:     true,
		CommandRules:    []string{"+@all"},
		CommandAllow:    make(map[string]bool),
		AllKeys:         true,
		KeyPatterns:     []string{"*"},
		AllChannels:     true,
		ChannelPatterns: []string{"*"},
		SanitizePayload: true,
	}

	return a
}

func newACLUser(name string) *ACLUser {
	return &ACLUser{
		Name:            name,
		Enabled:         false,
		NoPass:          false,
		CommandRules:    []string{"-@all"},
		CommandAllow:    make(map[string]bool),
		SanitizePayload: true,
	}
}

func cloneACLUser(u *ACLUser) *ACLUser {
	if u == nil {
		return nil
	}

	out := *u
	out.PasswordHashes = append([]string(nil), u.PasswordHashes...)
	out.CommandRules = append([]string(nil), u.CommandRules...)
	out.KeyPatterns = append([]string(nil), u.KeyPatterns...)
	out.ChannelPatterns = append(
		[]string(nil),
		u.ChannelPatterns...,
	)

	out.Selectors = make(
		[]ACLSelector,
		len(u.Selectors),
	)

	for i := range u.Selectors {
		out.Selectors[i] = cloneACLSelector(
			u.Selectors[i],
		)
	}

	out.CommandAllow = make(map[string]bool, len(u.CommandAllow))
	for name, allowed := range u.CommandAllow {
		out.CommandAllow[name] = allowed
	}

	return &out
}

func (a *ACL) GetUser(name string) (*ACLUser, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	u, ok := a.users[name]
	if !ok {
		return nil, false
	}

	return cloneACLUser(u), true
}

func (a *ACL) Users() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()

	names := make([]string, 0, len(a.users))
	for name := range a.users {
		names = append(names, name)
	}

	sort.Strings(names)
	return names
}

func aclPasswordHash(password string) string {
	sum := sha256.Sum256([]byte(password))
	return hex.EncodeToString(sum[:])
}

func constantTimeHexEqual(a, b string) bool {
	aa, err := hex.DecodeString(a)
	if err != nil {
		return false
	}

	bb, err := hex.DecodeString(b)
	if err != nil {
		return false
	}

	if len(aa) != len(bb) {
		return false
	}

	return subtle.ConstantTimeCompare(aa, bb) == 1
}

func (a *ACL) Authenticate(username, password string) bool {
	hash := aclPasswordHash(password)

	a.mu.RLock()
	defer a.mu.RUnlock()

	u, ok := a.users[username]
	if !ok || !u.Enabled {
		return false
	}

	if u.NoPass {
		return true
	}

	for _, candidate := range u.PasswordHashes {
		if constantTimeHexEqual(candidate, hash) {
			return true
		}
	}

	return false
}

func (a *ACL) DefaultUserNoPass() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	u, ok := a.users["default"]
	return ok && u.Enabled && u.NoPass
}

func validACLPasswordHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}

	for _, ch := range hash {
		if !((ch >= '0' && ch <= '9') ||
			(ch >= 'a' && ch <= 'f')) {
			return false
		}
	}

	return true
}

func removeACLPasswordHash(
	hashes []string,
	hash string,
) ([]string, bool) {
	for i, existing := range hashes {
		if existing != hash {
			continue
		}

		out := make([]string, 0, len(hashes)-1)
		out = append(out, hashes[:i]...)
		out = append(out, hashes[i+1:]...)

		return out, true
	}

	return hashes, false
}

func resetACLUserState(
	user *ACLUser,
	name string,
) {
	*user = ACLUser{
		Name:            name,
		Enabled:         false,
		NoPass:          false,
		PasswordHashes:  nil,
		AllCommands:     false,
		CommandRules:    []string{"-@all"},
		CommandAllow:    make(map[string]bool),
		AllKeys:         false,
		KeyPatterns:     nil,
		AllChannels:     false,
		ChannelPatterns: nil,
		SanitizePayload: true,
		Selectors:       nil,
	}
}

func (a *ACL) SetUser(name string, rules []string) error {
	if name == "" {
		return errors.New("ERR invalid username")
	}

	// Validate category names before changing any user state. Redis rejects
	// ACL SETUSER with an unknown category instead of partially applying the
	// preceding category rules.
	for _, rule := range rules {
		lower := strings.ToLower(rule)

		if strings.HasPrefix(lower, "+@") ||
			strings.HasPrefix(lower, "-@") {
			category := lower[2:]

			if category == "all" {
				continue
			}

			if _, ok := redisACLCategoryCommands[category]; !ok {
				return fmt.Errorf(
					"ERR Error in ACL SETUSER modifier '%s': Unknown command or category name in ACL",
					rule,
				)
			}
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	u, ok := a.users[name]
	if !ok {
		u = newACLUser(name)
		a.users[name] = u
	}

	for _, raw := range rules {
		rule := string(raw)

		if !strings.HasPrefix(rule, "(") &&
			strings.Contains(rule, ")") {
			return fmt.Errorf(
				"ERR Error in ACL SETUSER modifier '%s': Unknown command or category name in ACL",
				rule,
			)
		}

		if strings.HasPrefix(rule, "(") {
			selector, err := parseACLSelector(rule)
			if err != nil {
				return err
			}

			u.Selectors = append(
				u.Selectors,
				selector,
			)

			continue
		}

		switch {
		case strings.EqualFold(rule, "reset"):
			resetACLUserState(u, u.Name)

		case strings.EqualFold(rule, "on"):
			u.Enabled = true

		case strings.EqualFold(rule, "off"):
			u.Enabled = false

		case strings.EqualFold(rule, "nopass"):
			u.NoPass = true
			u.PasswordHashes = nil

		case strings.EqualFold(rule, "resetpass"):
			u.NoPass = false
			u.PasswordHashes = nil

		case strings.EqualFold(rule, "sanitize-payload"):
			u.SanitizePayload = true

		case strings.EqualFold(rule, "skip-sanitize-payload"):
			u.SanitizePayload = false

		case strings.EqualFold(rule, "resetchannels"):
			u.AllChannels = false
			u.ChannelPatterns = nil

		case strings.EqualFold(rule, "allchannels"):
			u.AllChannels = true
			u.ChannelPatterns = []string{"*"}

		case strings.HasPrefix(rule, "&"):
			// Redis accepts even a bare "&", which represents an empty
			// channel pattern.
			pattern := strings.TrimPrefix(rule, "&")

			if pattern == "*" {
				u.AllChannels = true
				u.ChannelPatterns = []string{"*"}
				break
			}

			if u.AllChannels {
				// &* already grants every channel. Keep the canonical
				// allchannels representation rather than adding redundant
				// patterns.
				break
			}

			found := false
			for _, existing := range u.ChannelPatterns {
				if existing == pattern {
					found = true
					break
				}
			}

			if !found {
				u.ChannelPatterns = append(
					u.ChannelPatterns,
					pattern,
				)
			}

		case strings.HasPrefix(rule, "<"):
			password := strings.TrimPrefix(rule, "<")

			sum := sha256.Sum256([]byte(password))
			hash := hex.EncodeToString(sum[:])

			var removed bool
			u.PasswordHashes, removed =
				removeACLPasswordHash(
					u.PasswordHashes,
					hash,
				)

			if !removed {
				return fmt.Errorf(
					"ERR Error in ACL SETUSER modifier '%s': The password you are trying to remove from the user does not exist",
					rule,
				)
			}

		case strings.HasPrefix(rule, "!"):
			hash := strings.TrimPrefix(rule, "!")

			if !validACLPasswordHash(hash) {
				return fmt.Errorf(
					"ERR Error in ACL SETUSER modifier '%s': The password hash must be exactly 64 characters and contain only lowercase hexadecimal characters",
					rule,
				)
			}

			var removed bool
			u.PasswordHashes, removed =
				removeACLPasswordHash(
					u.PasswordHashes,
					hash,
				)

			if !removed {
				return fmt.Errorf(
					"ERR Error in ACL SETUSER modifier '%s': The password you are trying to remove from the user does not exist",
					rule,
				)
			}

		case strings.HasPrefix(rule, "#"):
			hash := strings.TrimPrefix(rule, "#")

			if !validACLPasswordHash(hash) {
				return fmt.Errorf(
					"ERR Error in ACL SETUSER modifier '%s': The password hash must be exactly 64 characters and contain only lowercase hexadecimal characters",
					rule,
				)
			}

			u.NoPass = false

			exists := false
			for _, existing := range u.PasswordHashes {
				if existing == hash {
					exists = true
					break
				}
			}

			if !exists {
				u.PasswordHashes = append(
					u.PasswordHashes,
					hash,
				)
			}

		case strings.HasPrefix(rule, ">"):
			password := strings.TrimPrefix(rule, ">")

			sum := sha256.Sum256([]byte(password))
			hash := hex.EncodeToString(sum[:])

			u.NoPass = false

			exists := false
			for _, existing := range u.PasswordHashes {
				if existing == hash {
					exists = true
					break
				}
			}

			if !exists {
				u.PasswordHashes = append(
					u.PasswordHashes,
					hash,
				)
			}

		case strings.EqualFold(rule, "allkeys"):
			u.AllKeys = true
			u.KeyPatterns = []string{"*"}

		case strings.EqualFold(rule, "resetkeys"):
			u.AllKeys = false
			u.KeyPatterns = nil

		case strings.HasPrefix(rule, "~"):
			pattern := strings.TrimPrefix(rule, "~")
			if pattern == "" {
				return errors.New("ERR Error in ACL SETUSER modifier")
			}

			u.AllKeys = pattern == "*"

			if pattern == "*" {
				u.KeyPatterns = []string{"*"}
			} else {
				u.KeyPatterns = append(u.KeyPatterns, pattern)
			}

		case strings.EqualFold(rule, "allcommands"),
			strings.EqualFold(rule, "+@all"):
			u.AllCommands = true
			u.CommandAllow = make(map[string]bool)
			u.CommandRules = []string{"+@all"}

		case strings.EqualFold(rule, "nocommands"),
			strings.EqualFold(rule, "-@all"):
			u.AllCommands = false
			u.CommandAllow = make(map[string]bool)
			u.CommandRules = []string{"-@all"}

		case strings.HasPrefix(strings.ToLower(rule), "+@"):
			category := strings.ToLower(
				strings.TrimPrefix(
					strings.ToLower(rule),
					"+@",
				),
			)

			commands, ok := aclCommandsForCategory(category)
			if !ok {
				return fmt.Errorf(
					"ERR Error in ACL SETUSER modifier '%s': Unknown command or category name in ACL",
					rule,
				)
			}

			for _, command := range commands {
				u.CommandAllow[command] = true
			}

			u.CommandRules = append(
				u.CommandRules,
				"+@"+category,
			)

		case strings.HasPrefix(strings.ToLower(rule), "-@"):
			category := strings.ToLower(
				strings.TrimPrefix(
					strings.ToLower(rule),
					"-@",
				),
			)

			commands, ok := aclCommandsForCategory(category)
			if !ok {
				return fmt.Errorf(
					"ERR Error in ACL SETUSER modifier '%s': Unknown command or category name in ACL",
					rule,
				)
			}

			for _, command := range commands {
				u.CommandAllow[command] = false
			}

			u.CommandRules = append(
				u.CommandRules,
				"-@"+category,
			)

		case strings.HasPrefix(rule, "+"):
			command := strings.ToLower(strings.TrimPrefix(rule, "+"))
			if command == "" {
				return errors.New("ERR Error in ACL SETUSER modifier")
			}

			u.CommandAllow[command] = true
			u.CommandRules = append(u.CommandRules, "+"+command)

		case strings.HasPrefix(rule, "-"):
			command := strings.ToLower(strings.TrimPrefix(rule, "-"))
			if command == "" {
				return errors.New("ERR Error in ACL SETUSER modifier")
			}

			u.CommandAllow[command] = false
			u.CommandRules = append(u.CommandRules, "-"+command)

		default:
			return fmt.Errorf(
				"ERR Error in ACL SETUSER modifier '%s': Syntax error",
				rule,
			)
		}
	}

	return nil
}

func (a *ACL) DeleteUsers(names ...string) int64 {
	a.mu.Lock()
	defer a.mu.Unlock()

	var deleted int64

	for _, name := range names {
		if name == "default" {
			continue
		}

		if _, ok := a.users[name]; ok {
			delete(a.users, name)
			deleted++
		}
	}

	return deleted
}

func (a *ACL) CommandAllowed(username, command string) bool {
	command = strings.ToLower(command)

	a.mu.RLock()
	defer a.mu.RUnlock()

	u, ok := a.users[username]
	if !ok || !u.Enabled {
		return false
	}

	if allowed, exists := u.CommandAllow[command]; exists {
		return allowed
	}

	return u.AllCommands
}

func (a *ACL) KeyAllowed(username, key string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	u, ok := a.users[username]
	if !ok || !u.Enabled {
		return false
	}

	if u.AllKeys {
		return true
	}

	for _, pattern := range u.KeyPatterns {
		if aclGlobMatch(pattern, key) {
			return true
		}
	}

	return false
}

func (a *ACL) ChannelAllowed(
	username string,
	channel string,
) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	u, ok := a.users[username]
	if !ok || !u.Enabled {
		return false
	}

	if u.AllChannels {
		return true
	}

	for _, pattern := range u.ChannelPatterns {
		if aclGlobMatch(pattern, channel) {
			return true
		}
	}

	return false
}

// Redis treats PSUBSCRIBE differently from SUBSCRIBE/PUBLISH:
//
//	user: &allowed:*
//	PSUBSCRIBE allowed:*     -> allowed
//	PSUBSCRIBE allowed:foo*  -> denied
//	PSUBSCRIBE *             -> denied
//
// Therefore pattern subscriptions require an exact ACL pattern match,
// except for allchannels.
func (a *ACL) ChannelPatternAllowed(
	username string,
	pattern string,
) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	u, ok := a.users[username]
	if !ok || !u.Enabled {
		return false
	}

	if u.AllChannels {
		return true
	}

	for _, allowed := range u.ChannelPatterns {
		if allowed == pattern {
			return true
		}
	}

	return false
}

// Minimal Redis-style glob matcher.
// Supports '*' and '?' which covers the ACL v1 audit surface.
func aclGlobMatch(pattern, value string) bool {
	p := []rune(pattern)
	v := []rune(value)

	var match func(pi, vi int) bool

	match = func(pi, vi int) bool {
		for pi < len(p) {
			switch p[pi] {
			case '*':
				for pi+1 < len(p) && p[pi+1] == '*' {
					pi++
				}

				if pi+1 == len(p) {
					return true
				}

				for i := vi; i <= len(v); i++ {
					if match(pi+1, i) {
						return true
					}
				}

				return false

			case '?':
				if vi >= len(v) {
					return false
				}

				pi++
				vi++

			default:
				if vi >= len(v) || p[pi] != v[vi] {
					return false
				}

				pi++
				vi++
			}
		}

		return vi == len(v)
	}

	return match(0, 0)
}

func aclCommandsForCategory(category string) ([]string, bool) {
	redisCommands, ok := redisACLCategoryCommands[strings.ToLower(category)]
	if !ok {
		return nil, false
	}

	seen := make(map[string]struct{})

	for redisCommand := range redisCommands {
		command := strings.ToLower(redisCommand)

		if separator := strings.IndexByte(command, '|'); separator >= 0 {
			parent := command[:separator]
			subcommand := command[separator+1:]

			// ACL authorization is granular by subcommand.
			if parent == "acl" {
				if aclSubcommandSupported(subcommand) {
					seen[command] = struct{}{}
				}

				continue
			}

			// Other command families are currently authorized
			// at their top-level command name.
			if _, supported := commandTable[strings.ToUpper(parent)]; supported {
				seen[parent] = struct{}{}
			}

			continue
		}

		if _, supported := commandTable[strings.ToUpper(command)]; supported {
			seen[command] = struct{}{}
		}
	}

	commands := make([]string, 0, len(seen))

	for command := range seen {
		commands = append(commands, command)
	}

	sort.Strings(commands)

	return commands, true
}

func aclSubcommandSupported(subcommand string) bool {
	switch strings.ToLower(subcommand) {
	case "whoami",
		"users",
		"getuser",
		"list",
		"setuser",
		"deluser",
		"cat",
		"dryrun",
		"genpass",
		"log",
		"save",
		"load",
		"help":
		return true

	default:
		return false
	}
}

func (a *ACL) ReplaceFrom(source *ACL) {
	source.mu.RLock()
	defer source.mu.RUnlock()

	users := make(map[string]*ACLUser, len(source.users))

	for name, user := range source.users {
		users[name] = cloneACLUser(user)
	}

	a.mu.Lock()
	a.users = users
	a.mu.Unlock()
}
