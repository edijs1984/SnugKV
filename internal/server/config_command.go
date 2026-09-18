package server

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
)

type runtimeConfigEntry struct {
	name  string
	value func(*Server) string
}

var runtimeConfigEntries = []runtimeConfigEntry{
	{
		name: "maxmemory",
		value: func(s *Server) string {
			return strconv.FormatUint(
				s.store.MaxMemory(),
				10,
			)
		},
	},
	{
		name: "maxmemory-policy",
		value: func(s *Server) string {
			if s.eviction == "" {
				return "noeviction"
			}

			return s.eviction
		},
	},
	{
		name: "maxclients",
		value: func(s *Server) string {
			if s.configGetMaxClients == nil {
				return "10000"
			}

			return strconv.Itoa(
				s.configGetMaxClients(),
			)
		},
	},
	{
		name: "appendfsync",
		value: func(s *Server) string {

			s.configMu.RLock()
			defer s.configMu.RUnlock()

			if s.configAppendFsync == "" {
				return "everysec"
			}

			return s.configAppendFsync
		},
	},
	{
		name: "appendonly",
		value: func(s *Server) string {
			if s.configAppendOnly {
				return "yes"
			}

			return "no"
		},
	},
	{
		name: "databases",
		value: func(*Server) string {
			return "1"
		},
	},
}

func (s *Server) executeConfig(
	args [][]byte,
) ([]byte, error) {
	subcommand := strings.ToUpper(
		string(args[1]),
	)

	switch subcommand {
	case "HELP":
		if len(args) != 2 {
			return nil, errors.New(
				"ERR wrong number of arguments for 'config|help' command",
			)
		}

		return configHelpReply(), nil

	case "GET":
		if len(args) < 3 {
			return nil, errors.New(
				"ERR wrong number of arguments for 'config|get' command",
			)
		}

		return s.configGet(args[2:]), nil

	case "SET":
		if len(args) < 4 ||
			(len(args)-2)%2 != 0 {

			return nil, errors.New(
				"ERR wrong number of arguments for 'config|set' command",
			)
		}

		updates := make(
			[]func() error,
			0,
			(len(args)-2)/2,
		)

		for i := 2; i < len(args); i += 2 {
			apply, err := s.prepareConfigSet(
				string(args[i]),
				string(args[i+1]),
			)
			if err != nil {
				return nil, err
			}

			updates = append(
				updates,
				apply,
			)
		}

		for _, apply := range updates {
			if err := apply(); err != nil {
				return nil, err
			}
		}

		return []byte("+OK\r\n"), nil

	case "RESETSTAT":
		if len(args) != 2 {
			return nil, errors.New(
				"ERR wrong number of arguments for 'config|resetstat' command",
			)
		}

		atomic.StoreUint64(
			&s.commands,
			0,
		)

		if s.metrics != nil {
			s.metrics.Reset()
		}

		return []byte("+OK\r\n"), nil

	case "REWRITE":
		if len(args) != 2 {
			return nil, errors.New(
				"ERR wrong number of arguments for 'config|rewrite' command",
			)
		}

		// SnugKV currently does not retain the source JSON config path
		// inside the runtime server. Do not claim persistence occurred.
		return nil, errors.New(
			"ERR The server is running without a config file",
		)

	default:
		return nil, fmt.Errorf(
			"ERR unknown subcommand '%s'. Try CONFIG HELP.",
			subcommand,
		)
	}
}

func (s *Server) configGet(
	patternArgs [][]byte,
) []byte {
	items := make([][]byte, 0)

	// Redis accepts multiple patterns. A parameter matching more than
	// one requested pattern is returned only once.
	seen := make(map[string]struct{})

	for _, rawPattern := range patternArgs {
		pattern := strings.ToLower(
			string(rawPattern),
		)

		for _, entry := range runtimeConfigEntries {
			if _, ok := seen[entry.name]; ok {
				continue
			}

			if !configPatternMatch(
				pattern,
				entry.name,
			) {
				continue
			}

			seen[entry.name] = struct{}{}

			items = append(
				items,
				formatBulkString(
					[]byte(entry.name),
				),
				formatBulkString(
					[]byte(entry.value(s)),
				),
			)
		}
	}

	return array(items...)
}

func configPatternMatch(
	pattern string,
	name string,
) bool {
	matched, err := path.Match(
		pattern,
		name,
	)

	if err != nil {
		// Redis treats malformed glob-like CONFIG GET patterns as
		// ordinary non-matches rather than command syntax errors.
		return false
	}

	return matched
}

func (s *Server) configSet(
	rawName string,
	value string,
) error {
	apply, err := s.prepareConfigSet(
		rawName,
		value,
	)
	if err != nil {
		return err
	}

	return apply()
}

func (s *Server) prepareConfigSet(
	rawName string,
	value string,
) (func() error, error) {
	name := strings.ToLower(rawName)

	switch name {
	case "maxmemory":
		max, err := parseConfigMemory(value)

		if err != nil {
			return nil, errors.New(
				"ERR CONFIG SET failed (possibly related to argument 'maxmemory') - argument must be a memory value",
			)
		}

		return func() error {
			s.store.SetMaxMemory(max)
			return nil
		}, nil

	case "maxmemory-policy":
		policy := strings.ToLower(value)

		switch policy {
		case "noeviction",
			"allkeys-lru",
			"volatile-lru":

			return func() error {
				s.eviction = policy
				return nil
			}, nil

		default:
			return nil, errors.New(
				"ERR CONFIG SET failed (possibly related to argument 'maxmemory-policy') - argument(s) must be one of the following: volatile-lru, allkeys-lru, noeviction",
			)
		}

	case "maxclients":
		value64, err := strconv.ParseUint(
			value,
			10,
			32,
		)

		if err != nil ||
			value64 < 1 ||
			value64 > 4294967295 {

			return nil, errors.New(
				"ERR CONFIG SET failed (possibly related to argument 'maxclients') - argument must be between 1 and 4294967295 inclusive",
			)
		}

		if s.configSetMaxClients == nil {
			return nil, errors.New(
				"ERR CONFIG SET failed (possibly related to argument 'maxclients')",
			)
		}

		max := int(value64)

		return func() error {
			s.configSetMaxClients(max)
			return nil
		}, nil

	case "appendfsync":
		policy := strings.ToLower(value)

		switch policy {
		case "always", "everysec", "no":
		default:
			return nil, errors.New(
				"ERR CONFIG SET failed (possibly related to argument 'appendfsync') - argument(s) must be one of the following: always, everysec, no",
			)
		}

		if s.configSetAppendFsync == nil {
			return nil, errors.New(
				"ERR CONFIG SET failed (possibly related to argument 'appendfsync')",
			)
		}

		return func() error {
			return s.configSetAppendFsync(policy)
		}, nil

	default:
		return nil, fmt.Errorf(
			"ERR Unknown option or number of arguments for CONFIG SET - '%s'",
			rawName,
		)
	}
}

func parseConfigMemory(
	value string,
) (uint64, error) {
	if value == "" ||
		strings.HasPrefix(value, "-") {

		return 0, errors.New(
			"invalid memory value",
		)
	}

	lower := strings.ToLower(value)

	multiplier := uint64(1)
	number := lower

	suffixes := []struct {
		suffix     string
		multiplier uint64
	}{
		{"gb", 1024 * 1024 * 1024},
		{"g", 1024 * 1024 * 1024},
		{"mb", 1024 * 1024},
		{"m", 1024 * 1024},
		{"kb", 1024},
		{"k", 1024},
		{"b", 1},
	}

	for _, candidate := range suffixes {
		if strings.HasSuffix(
			lower,
			candidate.suffix,
		) {
			multiplier = candidate.multiplier
			number = strings.TrimSuffix(
				lower,
				candidate.suffix,
			)
			break
		}
	}

	if number == "" {
		return 0, errors.New(
			"invalid memory value",
		)
	}

	base, err := strconv.ParseUint(
		number,
		10,
		64,
	)
	if err != nil {
		return 0, err
	}

	if base > ^uint64(0)/multiplier {
		return 0, errors.New(
			"memory value overflow",
		)
	}

	return base * multiplier, nil
}

func configHelpReply() []byte {
	return array(
		formatBulkString(
			[]byte(
				"CONFIG <subcommand> [<arg> [value] [opt] ...]. Subcommands are:",
			),
		),
		formatBulkString(
			[]byte(
				"GET <pattern>",
			),
		),
		formatBulkString(
			[]byte(
				"    Return parameters matching the glob-like <pattern> and their values.",
			),
		),
		formatBulkString(
			[]byte(
				"SET <directive> <value>",
			),
		),
		formatBulkString(
			[]byte(
				"    Set the configuration <directive> to <value>.",
			),
		),
		formatBulkString(
			[]byte(
				"RESETSTAT",
			),
		),
		formatBulkString(
			[]byte(
				"    Reset statistics reported by the INFO command.",
			),
		),
		formatBulkString(
			[]byte(
				"REWRITE",
			),
		),
		formatBulkString(
			[]byte(
				"    Rewrite the configuration file.",
			),
		),
		formatBulkString(
			[]byte(
				"HELP",
			),
		),
		formatBulkString(
			[]byte(
				"    Print this help.",
			),
		),
	)
}
