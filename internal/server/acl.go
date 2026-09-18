package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
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
		Name:           "default",
		Enabled:        true,
		NoPass:         true,
		PasswordHashes: nil,
		AllCommands:    true,
		CommandRules:   []string{"+@all"},
		CommandAllow:   make(map[string]bool),
		AllKeys:        true,
		KeyPatterns:    []string{"*"},
	}

	return a
}

func newACLUser(name string) *ACLUser {
	return &ACLUser{
		Name:         name,
		Enabled:      false,
		NoPass:       false,
		CommandRules: []string{"-@all"},
		CommandAllow: make(map[string]bool),
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

func (a *ACL) SetUser(name string, rules []string) error {
	if name == "" {
		return errors.New("ERR invalid username")
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

		switch {
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

		case strings.HasPrefix(rule, ">"):
			password := strings.TrimPrefix(rule, ">")
			if password == "" {
				return errors.New("ERR Error in ACL SETUSER modifier")
			}

			u.NoPass = false

			hash := aclPasswordHash(password)
			found := false

			for _, existing := range u.PasswordHashes {
				if existing == hash {
					found = true
					break
				}
			}

			if !found {
				u.PasswordHashes = append(u.PasswordHashes, hash)
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

		case strings.EqualFold(rule, "+@all"):
			u.AllCommands = true
			u.CommandAllow = make(map[string]bool)
			u.CommandRules = []string{"+@all"}

		case strings.EqualFold(rule, "-@all"):
			u.AllCommands = false
			u.CommandAllow = make(map[string]bool)
			u.CommandRules = []string{"-@all"}

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
			return errors.New("ERR Error in ACL SETUSER modifier")
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
