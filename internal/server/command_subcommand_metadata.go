package server

import (
	"sort"
	"strings"
)

type commandLeafMetadata struct {
	arity int
	flags []string
	acl   []string
	tips  []string
}

type commandParentMetadata struct {
	arity       int
	flags       []string
	acl         []string
	tips        []string
	subcommands []string
}

var commandParentMetadataTable = map[string]commandParentMetadata{
	"COMMAND": {
		arity: -1,
		flags: []string{
			"loading",
			"stale",
		},
		acl: []string{
			"@slow",
			"@connection",
		},
		tips: []string{
			"nondeterministic_output_order",
		},
		subcommands: []string{
			"COMMAND|DOCS",
			"COMMAND|GETKEYSANDFLAGS",
			"COMMAND|INFO",
			"COMMAND|COUNT",
			"COMMAND|GETKEYS",
		},
	},

	"CLIENT": {
		arity: -2,
		acl: []string{
			"@slow",
		},
		subcommands: []string{
			"CLIENT|UNBLOCK",
			"CLIENT|INFO",
			"CLIENT|ID",
			"CLIENT|KILL",
			"CLIENT|SETNAME",
			"CLIENT|LIST",
			"CLIENT|GETNAME",
			"CLIENT|HELP",
			"CLIENT|SETINFO",
			"CLIENT|TRACKING",
			"CLIENT|CACHING",
			"CLIENT|GETREDIR",
		},
	},

	"FUNCTION": {
		arity: -2,
		acl: []string{
			"@slow",
		},
		subcommands: []string{
			"FUNCTION|HELP",
			"FUNCTION|LOAD",
			"FUNCTION|DUMP",
			"FUNCTION|KILL",
			"FUNCTION|STATS",
			"FUNCTION|FLUSH",
			"FUNCTION|LIST",
			"FUNCTION|RESTORE",
			"FUNCTION|DELETE",
		},
	},

	"SCRIPT": {
		arity: -2,
		acl: []string{
			"@slow",
		},
		subcommands: []string{
			"SCRIPT|KILL",
			"SCRIPT|EXISTS",
			"SCRIPT|LOAD",
			"SCRIPT|FLUSH",
			"SCRIPT|DEBUG",
		},
	},
}

var commandLeafMetadataTable = map[string]commandLeafMetadata{
	// --------------------------------------------------------
	// COMMAND
	// --------------------------------------------------------

	"COMMAND|COUNT": {
		arity: 2,
		flags: []string{"loading", "stale"},
		acl:   []string{"@slow", "@connection"},
	},

	"COMMAND|INFO": {
		arity: -2,
		flags: []string{"loading", "stale"},
		acl:   []string{"@slow", "@connection"},
		tips:  []string{"nondeterministic_output_order"},
	},

	"COMMAND|DOCS": {
		arity: -2,
		flags: []string{"loading", "stale"},
		acl:   []string{"@slow", "@connection"},
		tips:  []string{"nondeterministic_output_order"},
	},

	"COMMAND|GETKEYS": {
		arity: -3,
		flags: []string{"loading", "stale"},
		acl:   []string{"@slow", "@connection"},
	},

	"COMMAND|GETKEYSANDFLAGS": {
		arity: -3,
		flags: []string{"loading", "stale"},
		acl:   []string{"@slow", "@connection"},
	},

	// --------------------------------------------------------
	// CLIENT
	// --------------------------------------------------------

	"CLIENT|ID": {
		arity: 2,
		flags: []string{"noscript", "loading", "stale"},
		acl:   []string{"@slow", "@connection"},
	},

	"CLIENT|GETNAME": {
		arity: 2,
		flags: []string{"noscript", "loading", "stale"},
		acl:   []string{"@slow", "@connection"},
	},

	"CLIENT|SETNAME": {
		arity: 3,
		flags: []string{"noscript", "loading", "stale"},
		acl:   []string{"@slow", "@connection"},
		tips: []string{
			"request_policy:all_nodes",
			"response_policy:all_succeeded",
		},
	},

	"CLIENT|SETINFO": {
		arity: 4,
		flags: []string{"noscript", "loading", "stale"},
		acl:   []string{"@slow", "@connection"},
		tips: []string{
			"request_policy:all_nodes",
			"response_policy:all_succeeded",
		},
	},

	"CLIENT|INFO": {
		arity: 2,
		flags: []string{"noscript", "loading", "stale"},
		acl:   []string{"@slow", "@connection"},
		tips:  []string{"nondeterministic_output"},
	},

	"CLIENT|TRACKING": {
		arity: -3,
		flags: []string{"noscript", "loading", "stale"},
		acl:   []string{"@slow", "@connection"},
	},

	"CLIENT|CACHING": {
		arity: 3,
		flags: []string{"noscript", "loading", "stale"},
		acl:   []string{"@slow", "@connection"},
	},

	"CLIENT|GETREDIR": {
		arity: 2,
		flags: []string{"noscript", "loading", "stale"},
		acl:   []string{"@slow", "@connection"},
	},

	"CLIENT|LIST": {
		arity: -2,
		flags: []string{
			"admin",
			"noscript",
			"loading",
			"stale",
		},
		acl: []string{
			"@admin",
			"@slow",
			"@dangerous",
			"@connection",
		},
		tips: []string{"nondeterministic_output"},
	},

	"CLIENT|KILL": {
		arity: -3,
		flags: []string{
			"admin",
			"noscript",
			"loading",
			"stale",
		},
		acl: []string{
			"@admin",
			"@slow",
			"@dangerous",
			"@connection",
		},
	},

	"CLIENT|UNBLOCK": {
		arity: -3,
		flags: []string{
			"admin",
			"noscript",
			"loading",
			"stale",
		},
		acl: []string{
			"@admin",
			"@slow",
			"@dangerous",
			"@connection",
		},
	},

	"CLIENT|HELP": {
		arity: 2,
		flags: []string{"loading", "stale"},
		acl:   []string{"@slow", "@connection"},
	},

	// --------------------------------------------------------
	// FUNCTION
	// --------------------------------------------------------

	"FUNCTION|LOAD": {
		arity: -3,
		flags: []string{"write", "denyoom", "noscript"},
		acl:   []string{"@write", "@slow", "@scripting"},
		tips: []string{
			"request_policy:all_shards",
			"response_policy:all_succeeded",
		},
	},

	"FUNCTION|LIST": {
		arity: -2,
		flags: []string{"noscript"},
		acl:   []string{"@slow", "@scripting"},
		tips:  []string{"nondeterministic_output_order"},
	},

	"FUNCTION|DELETE": {
		arity: 3,
		flags: []string{"write", "noscript"},
		acl:   []string{"@write", "@slow", "@scripting"},
		tips: []string{
			"request_policy:all_shards",
			"response_policy:all_succeeded",
		},
	},

	"FUNCTION|FLUSH": {
		arity: -2,
		flags: []string{"write", "noscript"},
		acl:   []string{"@write", "@slow", "@scripting"},
		tips: []string{
			"request_policy:all_shards",
			"response_policy:all_succeeded",
		},
	},

	"FUNCTION|DUMP": {
		arity: 2,
		flags: []string{"noscript"},
		acl:   []string{"@slow", "@scripting"},
	},

	"FUNCTION|RESTORE": {
		arity: -3,
		flags: []string{"write", "denyoom", "noscript"},
		acl:   []string{"@write", "@slow", "@scripting"},
		tips: []string{
			"request_policy:all_shards",
			"response_policy:all_succeeded",
		},
	},

	"FUNCTION|STATS": {
		arity: 2,
		flags: []string{"noscript", "allow_busy"},
		acl:   []string{"@slow", "@scripting"},
		tips: []string{
			"nondeterministic_output",
			"request_policy:all_shards",
			"response_policy:special",
		},
	},

	"FUNCTION|KILL": {
		arity: 2,
		flags: []string{"noscript", "allow_busy"},
		acl:   []string{"@slow", "@scripting"},
		tips: []string{
			"request_policy:all_shards",
			"response_policy:one_succeeded",
		},
	},

	"FUNCTION|HELP": {
		arity: 2,
		flags: []string{"loading", "stale"},
		acl:   []string{"@slow", "@scripting"},
	},

	// --------------------------------------------------------
	// SCRIPT
	// --------------------------------------------------------

	"SCRIPT|LOAD": {
		arity: 3,
		flags: []string{"noscript", "stale"},
		acl:   []string{"@slow", "@scripting"},
		tips: []string{
			"request_policy:all_nodes",
			"response_policy:all_succeeded",
		},
	},

	"SCRIPT|EXISTS": {
		arity: -3,
		flags: []string{"noscript"},
		acl:   []string{"@slow", "@scripting"},
		tips: []string{
			"request_policy:all_shards",
			"response_policy:agg_logical_and",
		},
	},

	"SCRIPT|FLUSH": {
		arity: -2,
		flags: []string{"noscript"},
		acl:   []string{"@slow", "@scripting"},
		tips: []string{
			"request_policy:all_nodes",
			"response_policy:all_succeeded",
		},
	},

	"SCRIPT|KILL": {
		arity: 2,
		flags: []string{"noscript", "allow_busy"},
		acl:   []string{"@slow", "@scripting"},
		tips: []string{
			"request_policy:all_shards",
			"response_policy:one_succeeded",
		},
	},

	"SCRIPT|DEBUG": {
		arity: 3,
		flags: []string{"noscript"},
		acl:   []string{"@slow", "@scripting"},
	},
}

func commandStructuredInfoReply(
	name string,
	arity int,
	flags []string,
	acl []string,
	tips []string,
	subcommands []string,
) []byte {
	children := make([][]byte, 0, len(subcommands))

	for _, child := range subcommands {
		meta, ok := commandLeafMetadataTable[child]
		if !ok {
			continue
		}

		children = append(
			children,
			commandStructuredInfoReply(
				child,
				meta.arity,
				meta.flags,
				meta.acl,
				meta.tips,
				nil,
			),
		)
	}

	return array(
		formatBulkString(
			[]byte(strings.ToLower(name)),
		),
		integer(int64(arity)),
		commandRESPStrings(flags),
		integer(0),
		integer(0),
		integer(0),
		commandRESPStrings(acl),
		commandBulkStrings(tips),
		array(),
		array(children...),
	)
}

func commandInfoSupported(name string) bool {
	upper := strings.ToUpper(name)

	if _, ok := commandLeafMetadataTable[upper]; ok {
		return true
	}

	if _, ok := commandParentMetadataTable[upper]; ok {
		return true
	}

	_, ok := commandTable[upper]
	return ok
}

func commandInfoReply(name string) []byte {
	upper := strings.ToUpper(name)

	if meta, ok := commandLeafMetadataTable[upper]; ok {
		return commandStructuredInfoReply(
			upper,
			meta.arity,
			meta.flags,
			meta.acl,
			meta.tips,
			nil,
		)
	}

	if meta, ok := commandParentMetadataTable[upper]; ok {
		return commandStructuredInfoReply(
			upper,
			meta.arity,
			meta.flags,
			meta.acl,
			meta.tips,
			meta.subcommands,
		)
	}

	return commandInfoLegacyReply(name)
}

// -----------------------------------------------------------------------------
// COMMAND DOCS hierarchy
// -----------------------------------------------------------------------------

type commandContainerDoc struct {
	summary     string
	since       string
	group       string
	complexity  string
	subcommands []string
}

var commandContainerDocs = map[string]commandContainerDoc{
	"COMMAND": {
		summary:    "Returns detailed information about all commands.",
		since:      "2.8.13",
		group:      "server",
		complexity: "O(N) where N is the total number of Redis commands",
		subcommands: []string{
			"COMMAND|DOCS",
			"COMMAND|GETKEYSANDFLAGS",
			"COMMAND|INFO",
			"COMMAND|COUNT",
			"COMMAND|GETKEYS",
		},
	},

	"CLIENT": {
		summary:    "A container for client connection commands.",
		since:      "2.4.0",
		group:      "connection",
		complexity: "Depends on subcommand.",
		subcommands: []string{
			"CLIENT|UNBLOCK",
			"CLIENT|INFO",
			"CLIENT|ID",
			"CLIENT|KILL",
			"CLIENT|SETNAME",
			"CLIENT|LIST",
			"CLIENT|GETNAME",
			"CLIENT|HELP",
			"CLIENT|SETINFO",
			"CLIENT|TRACKING",
			"CLIENT|CACHING",
			"CLIENT|GETREDIR",
		},
	},

	"FUNCTION": {
		summary:    "A container for function commands.",
		since:      "7.0.0",
		group:      "scripting",
		complexity: "Depends on subcommand.",
		subcommands: []string{
			"FUNCTION|HELP",
			"FUNCTION|LOAD",
			"FUNCTION|DUMP",
			"FUNCTION|KILL",
			"FUNCTION|STATS",
			"FUNCTION|FLUSH",
			"FUNCTION|LIST",
			"FUNCTION|RESTORE",
			"FUNCTION|DELETE",
		},
	},

	"SCRIPT": {
		summary:    "A container for Lua scripts management commands.",
		since:      "2.6.0",
		group:      "scripting",
		complexity: "Depends on subcommand.",
		subcommands: []string{
			"SCRIPT|KILL",
			"SCRIPT|EXISTS",
			"SCRIPT|LOAD",
			"SCRIPT|FLUSH",
			"SCRIPT|DEBUG",
		},
	},
}

func pureToken(
	name string,
	token string,
) commandDocArgument {
	return commandDocArgument{
		name:        name,
		displayText: name,
		argType:     "pure-token",
		token:       token,
	}
}

var commandSubcommandDocs = map[string]commandDoc{
	// COMMAND -------------------------------------------------

	"COMMAND|COUNT": {
		summary:    "Returns a count of commands.",
		since:      "2.8.13",
		group:      "server",
		complexity: "O(1)",
	},

	"COMMAND|INFO": {
		summary:    "Returns information about one, multiple or all commands.",
		since:      "2.8.13",
		group:      "server",
		complexity: "O(N) where N is the number of commands to look up",
		history: [][2]string{
			{
				"7.0.0",
				"Allowed to be called with no argument to get info on all commands.",
			},
		},
		arguments: []commandDocArgument{
			{
				name:        "command-name",
				displayText: "command-name",
				argType:     "string",
				optional:    true,
				multiple:    true,
			},
		},
	},

	"COMMAND|DOCS": {
		summary:    "Returns documentary information about one, multiple or all commands.",
		since:      "7.0.0",
		group:      "server",
		complexity: "O(N) where N is the number of commands to look up",
		arguments: []commandDocArgument{
			{
				name:        "command-name",
				displayText: "command-name",
				argType:     "string",
				optional:    true,
				multiple:    true,
			},
		},
	},

	"COMMAND|GETKEYS": {
		summary:    "Extracts the key names from an arbitrary command.",
		since:      "2.8.13",
		group:      "server",
		complexity: "O(N) where N is the number of arguments to the command",
		arguments: []commandDocArgument{
			{
				name:        "command",
				displayText: "command",
				argType:     "string",
			},
			{
				name:        "arg",
				displayText: "arg",
				argType:     "string",
				optional:    true,
				multiple:    true,
			},
		},
	},

	"COMMAND|GETKEYSANDFLAGS": {
		summary:    "Extracts the key names and access flags for an arbitrary command.",
		since:      "7.0.0",
		group:      "server",
		complexity: "O(N) where N is the number of arguments to the command",
		arguments: []commandDocArgument{
			{
				name:        "command",
				displayText: "command",
				argType:     "string",
			},
			{
				name:        "arg",
				displayText: "arg",
				argType:     "string",
				optional:    true,
				multiple:    true,
			},
		},
	},

	// CLIENT --------------------------------------------------

	"CLIENT|ID": {
		summary:    "Returns the unique client ID of the connection.",
		since:      "5.0.0",
		group:      "connection",
		complexity: "O(1)",
	},

	"CLIENT|GETNAME": {
		summary:    "Returns the name of the connection.",
		since:      "2.6.9",
		group:      "connection",
		complexity: "O(1)",
	},

	"CLIENT|SETNAME": {
		summary:    "Sets the connection name.",
		since:      "2.6.9",
		group:      "connection",
		complexity: "O(1)",
		arguments: []commandDocArgument{
			{
				name:        "connection-name",
				displayText: "connection-name",
				argType:     "string",
			},
		},
	},

	"CLIENT|SETINFO": {
		summary:    "Sets information specific to the client or connection.",
		since:      "7.2.0",
		group:      "connection",
		complexity: "O(1)",
		arguments: []commandDocArgument{
			{
				name:    "attr",
				argType: "oneof",
				arguments: []commandDocArgument{
					{
						name:        "libname",
						displayText: "libname",
						argType:     "string",
						token:       "LIB-NAME",
					},
					{
						name:        "libver",
						displayText: "libver",
						argType:     "string",
						token:       "LIB-VER",
					},
				},
			},
		},
	},

	"CLIENT|INFO": {
		summary:    "Returns information about the connection.",
		since:      "6.2.0",
		group:      "connection",
		complexity: "O(1)",
	},

	"CLIENT|TRACKING": {
		summary:    "Controls server-assisted client-side caching for the current connection.",
		since:      "6.0.0",
		group:      "connection",
		complexity: "O(1)",
	},

	"CLIENT|CACHING": {
		summary:    "Controls whether the next command is cached when tracking uses OPTIN or OPTOUT mode.",
		since:      "6.0.0",
		group:      "connection",
		complexity: "O(1)",
		arguments: []commandDocArgument{
			{
				name:    "mode",
				argType: "oneof",
				arguments: []commandDocArgument{
					pureToken("yes", "YES"),
					pureToken("no", "NO"),
				},
			},
		},
	},

	"CLIENT|GETREDIR": {
		summary:    "Returns the client ID used as the invalidation redirect target, or -1 if redirection is disabled.",
		since:      "6.0.0",
		group:      "connection",
		complexity: "O(1)",
	},

	"CLIENT|LIST": {
		summary:    "Lists open connections.",
		since:      "2.4.0",
		group:      "connection",
		complexity: "O(N) where N is the number of client connections",
		arguments: []commandDocArgument{
			{
				name:     "client-type",
				argType:  "oneof",
				token:    "TYPE",
				since:    "5.0.0",
				optional: true,
				arguments: []commandDocArgument{
					pureToken(
						"normal",
						"NORMAL",
					),
				},
			},
			{
				name:        "client-id",
				displayText: "client-id",
				argType:     "integer",
				token:       "ID",
				since:       "6.2.0",
				optional:    true,
				multiple:    true,
			},
		},
	},

	"CLIENT|KILL": {
		summary:    "Terminates open connections.",
		since:      "2.4.0",
		group:      "connection",
		complexity: "O(N) where N is the number of client connections",
		arguments: []commandDocArgument{
			{
				name:        "client-id",
				displayText: "client-id",
				argType:     "integer",
				token:       "ID",
			},
			{
				name:     "skipme",
				argType:  "oneof",
				token:    "SKIPME",
				optional: true,
				arguments: []commandDocArgument{
					pureToken("yes", "YES"),
					pureToken("no", "NO"),
				},
			},
		},
	},

	"CLIENT|UNBLOCK": {
		summary:    "Unblocks a client blocked by a blocking command from a different connection.",
		since:      "5.0.0",
		group:      "connection",
		complexity: "O(log N) where N is the number of client connections",
		arguments: []commandDocArgument{
			{
				name:        "client-id",
				displayText: "client-id",
				argType:     "integer",
			},
			{
				name:     "unblock-type",
				argType:  "oneof",
				optional: true,
				arguments: []commandDocArgument{
					pureToken(
						"timeout",
						"TIMEOUT",
					),
					pureToken(
						"error",
						"ERROR",
					),
				},
			},
		},
	},

	"CLIENT|HELP": {
		summary:    "Returns helpful text about the different subcommands.",
		since:      "5.0.0",
		group:      "connection",
		complexity: "O(1)",
	},

	// FUNCTION ------------------------------------------------

	"FUNCTION|LOAD": {
		summary:    "Creates a library.",
		since:      "7.0.0",
		group:      "scripting",
		complexity: "O(1) (considering compilation time is redundant)",
		arguments: []commandDocArgument{
			{
				name:        "replace",
				displayText: "replace",
				argType:     "pure-token",
				token:       "REPLACE",
				optional:    true,
			},
			{
				name:        "function-code",
				displayText: "function-code",
				argType:     "string",
			},
		},
	},

	"FUNCTION|LIST": {
		summary:    "Returns information about all libraries.",
		since:      "7.0.0",
		group:      "scripting",
		complexity: "O(N) where N is the number of functions",
		arguments: []commandDocArgument{
			{
				name:        "library-name-pattern",
				displayText: "library-name-pattern",
				argType:     "string",
				token:       "LIBRARYNAME",
				optional:    true,
			},
			{
				name:        "withcode",
				displayText: "withcode",
				argType:     "pure-token",
				token:       "WITHCODE",
				optional:    true,
			},
		},
	},

	"FUNCTION|DELETE": {
		summary:    "Deletes a library and its functions.",
		since:      "7.0.0",
		group:      "scripting",
		complexity: "O(1)",
		arguments: []commandDocArgument{
			{
				name:        "library-name",
				displayText: "library-name",
				argType:     "string",
			},
		},
	},

	"FUNCTION|FLUSH": {
		summary:    "Deletes all libraries and functions.",
		since:      "7.0.0",
		group:      "scripting",
		complexity: "O(N) where N is the number of functions deleted",
		arguments: []commandDocArgument{
			{
				name:     "flush-type",
				argType:  "oneof",
				optional: true,
				arguments: []commandDocArgument{
					pureToken("async", "ASYNC"),
					pureToken("sync", "SYNC"),
				},
			},
		},
	},

	"FUNCTION|DUMP": {
		summary:    "Dumps all libraries into a serialized binary payload.",
		since:      "7.0.0",
		group:      "scripting",
		complexity: "O(N) where N is the number of functions",
	},

	"FUNCTION|RESTORE": {
		summary:    "Restores all libraries from a payload.",
		since:      "7.0.0",
		group:      "scripting",
		complexity: "O(N) where N is the number of functions on the payload",
		arguments: []commandDocArgument{
			{
				name:        "serialized-value",
				displayText: "serialized-value",
				argType:     "string",
			},
			{
				name:     "policy",
				argType:  "oneof",
				optional: true,
				arguments: []commandDocArgument{
					pureToken("flush", "FLUSH"),
					pureToken("append", "APPEND"),
					pureToken("replace", "REPLACE"),
				},
			},
		},
	},

	"FUNCTION|STATS": {
		summary:    "Returns information about a function during execution.",
		since:      "7.0.0",
		group:      "scripting",
		complexity: "O(1)",
	},

	"FUNCTION|KILL": {
		summary:    "Terminates a function during execution.",
		since:      "7.0.0",
		group:      "scripting",
		complexity: "O(1)",
	},

	"FUNCTION|HELP": {
		summary:    "Returns helpful text about the different subcommands.",
		since:      "7.0.0",
		group:      "scripting",
		complexity: "O(1)",
	},

	// SCRIPT --------------------------------------------------

	"SCRIPT|LOAD": {
		summary:    "Loads a server-side Lua script to the script cache.",
		since:      "2.6.0",
		group:      "scripting",
		complexity: "O(N) with N being the length in bytes of the script body.",
		arguments: []commandDocArgument{
			{
				name:        "script",
				displayText: "script",
				argType:     "string",
			},
		},
	},

	"SCRIPT|EXISTS": {
		summary:    "Determines whether server-side Lua scripts exist in the script cache.",
		since:      "2.6.0",
		group:      "scripting",
		complexity: "O(N) with N being the number of scripts to check (so checking a single script is an O(1) operation).",
		arguments: []commandDocArgument{
			{
				name:        "sha1",
				displayText: "sha1",
				argType:     "string",
				multiple:    true,
			},
		},
	},

	"SCRIPT|FLUSH": {
		summary:    "Removes all server-side Lua scripts from the script cache.",
		since:      "2.6.0",
		group:      "scripting",
		complexity: "O(N) with N being the number of scripts in cache",
		history: [][2]string{
			{
				"6.2.0",
				"Added the `ASYNC` and `SYNC` flushing mode modifiers.",
			},
		},
		arguments: []commandDocArgument{
			{
				name:     "flush-type",
				argType:  "oneof",
				since:    "6.2.0",
				optional: true,
				arguments: []commandDocArgument{
					pureToken("async", "ASYNC"),
					pureToken("sync", "SYNC"),
				},
			},
		},
	},

	"SCRIPT|KILL": {
		summary:    "Terminates a server-side Lua script during execution.",
		since:      "2.6.0",
		group:      "scripting",
		complexity: "O(1)",
	},

	"SCRIPT|DEBUG": {
		summary:    "Sets the Lua debugging mode for the current connection.",
		since:      "3.2.0",
		group:      "scripting",
		complexity: "O(1)",
		arguments: []commandDocArgument{
			{
				name:    "mode",
				argType: "oneof",
				arguments: []commandDocArgument{
					pureToken("yes", "YES"),
					pureToken("sync", "SYNC"),
					pureToken("no", "NO"),
				},
			},
		},
	},
}

func commandContainerDocReply(
	doc commandContainerDoc,
) []byte {
	parts := [][]byte{
		formatBulkString([]byte("summary")),
		formatBulkString([]byte(doc.summary)),
		formatBulkString([]byte("since")),
		formatBulkString([]byte(doc.since)),
		formatBulkString([]byte("group")),
		formatBulkString([]byte(doc.group)),
		formatBulkString([]byte("complexity")),
		formatBulkString([]byte(doc.complexity)),
	}

	children := make(
		[][]byte,
		0,
		len(doc.subcommands)*2,
	)

	for _, child := range doc.subcommands {
		childDoc, ok := commandSubcommandDocs[child]
		if !ok {
			continue
		}

		children = append(
			children,
			formatBulkString(
				[]byte(strings.ToLower(child)),
			),
			commandDocReply(childDoc),
		)
	}

	parts = append(
		parts,
		formatBulkString([]byte("subcommands")),
		array(children...),
	)

	return array(parts...)
}

func commandDocsSupported(
	name string,
) bool {
	if _, ok := commandContainerDocs[name]; ok {
		return true
	}

	if _, ok := commandSubcommandDocs[name]; ok {
		return true
	}

	_, ok := commandDocs[name]
	if !ok {
		return false
	}

	_, ok = commandTable[name]
	return ok
}

func commandDocsItemReply(
	name string,
) ([]byte, bool) {
	if doc, ok := commandContainerDocs[name]; ok {
		return commandContainerDocReply(doc), true
	}

	if doc, ok := commandSubcommandDocs[name]; ok {
		return commandDocReply(doc), true
	}

	doc, ok := commandDocs[name]
	if !ok {
		return nil, false
	}

	if _, registered := commandTable[name]; !registered {
		return nil, false
	}

	return commandDocReply(doc), true
}

func commandDocsReply(names [][]byte) []byte {
	requested := make([]string, 0)

	if len(names) == 0 {
		seen := make(map[string]struct{})

		for name := range commandDocs {
			if strings.Contains(name, "|") {
				continue
			}

			if !commandDocsSupported(name) {
				continue
			}

			seen[name] = struct{}{}
		}

		for name := range commandContainerDocs {
			seen[name] = struct{}{}
		}

		requested = make(
			[]string,
			0,
			len(seen),
		)

		for name := range seen {
			requested = append(
				requested,
				name,
			)
		}

		sort.Strings(requested)
	} else {
		seen := make(
			map[string]struct{},
			len(names),
		)

		for _, raw := range names {
			name := strings.ToUpper(
				string(raw),
			)

			if _, exists := seen[name]; exists {
				continue
			}

			seen[name] = struct{}{}

			if !commandDocsSupported(name) {
				continue
			}

			requested = append(
				requested,
				name,
			)
		}
	}

	parts := make(
		[][]byte,
		0,
		len(requested)*2,
	)

	for _, name := range requested {
		reply, ok := commandDocsItemReply(name)
		if !ok {
			continue
		}

		parts = append(
			parts,
			formatBulkString(
				[]byte(strings.ToLower(name)),
			),
			reply,
		)
	}

	return array(parts...)
}
