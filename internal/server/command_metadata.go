package server

import (
	"errors"
	"strconv"
	"strings"
)

type commandKeyRef struct {
	value []byte
	flags []string
}

func commandInvocationInfo(args [][]byte) (string, commandInfo, error) {
	if len(args) == 0 {
		return "", commandInfo{}, errors.New("ERR Invalid command specified")
	}

	name := strings.ToUpper(string(args[0]))

	info, ok := commandTable[name]
	if !ok {
		return "", commandInfo{}, errors.New("ERR Invalid command specified")
	}

	if len(args) < info.min ||
		(info.max > 0 && len(args) > info.max) {
		return "", commandInfo{}, errors.New(
			"ERR Invalid number of arguments specified for command",
		)
	}

	return name, info, nil
}

func commandKeys(args [][]byte) ([]commandKeyRef, error) {
	name, info, err := commandInvocationInfo(args)
	if err != nil {
		return nil, err
	}

	switch name {
	case "EVAL", "EVALSHA", "EVAL_RO", "EVALSHA_RO",
		"FCALL", "FCALL_RO":
		return commandKeyCountKeys(name, args)

	case "ZUNION", "ZINTER", "ZDIFF", "ZINTERCARD",
		"ZUNIONSTORE", "ZINTERSTORE", "ZDIFFSTORE":
		return commandZSetAlgebraKeys(name, args)

	case "ZMPOP":
		return commandZMPopKeys(args)

	case "BZMPOP":
		return commandBZMPopKeys(args)

	case "XREAD":
		return commandXReadKeys(args)

	case "XREADGROUP":
		return commandXReadGroupKeys(args)

	case "COPY":
		if len(args) < 3 {
			return nil, errors.New(
				"ERR Invalid number of arguments specified for command",
			)
		}

		return []commandKeyRef{
			{
				value: args[1],
				flags: []string{"RO", "access"},
			},
			{
				value: args[2],
				flags: []string{"OW", "update"},
			},
		}, nil

	case "BITOP":
		if len(args) < 4 {
			return nil, errors.New(
				"ERR Invalid number of arguments specified for command",
			)
		}

		refs := make([]commandKeyRef, 0, len(args)-2)

		refs = append(refs, commandKeyRef{
			value: args[2],
			flags: []string{"OW", "update"},
		})

		for _, key := range args[3:] {
			refs = append(refs, commandKeyRef{
				value: key,
				flags: []string{"RO", "access"},
			})
		}

		return refs, nil
	}

	if info.first == 0 || info.step <= 0 {
		return nil, nil
	}

	last := info.last

	if last < 0 {
		last = len(args) + last
	}

	if last >= len(args) {
		last = len(args) - 1
	}

	if info.first >= len(args) || last < info.first {
		return nil, nil
	}

	refs := make(
		[]commandKeyRef,
		0,
		1+(last-info.first)/info.step,
	)

	for index := info.first; index <= last; index += info.step {
		refs = append(
			refs,
			commandKeyRef{
				value: args[index],
				flags: commandKeyFlags(
					name,
					index,
					info.write,
				),
			},
		)
	}

	return refs, nil
}

func commandZSetAlgebraKeys(
	name string,
	args [][]byte,
) ([]commandKeyRef, error) {
	request, err := parseZSetAlgebraRequest(args)
	if err != nil {
		return nil, err
	}

	store := strings.HasSuffix(name, "STORE")

	capacity := len(request.keys)
	if store {
		capacity++
	}

	refs := make([]commandKeyRef, 0, capacity)

	if store {
		refs = append(
			refs,
			commandKeyRef{
				value: []byte(request.destination),
				flags: []string{"OW", "update"},
			},
		)
	}

	for _, key := range request.keys {
		refs = append(
			refs,
			commandKeyRef{
				value: []byte(key),
				flags: []string{"RO", "access"},
			},
		)
	}

	return refs, nil
}

func commandZMPopKeys(
	args [][]byte,
) ([]commandKeyRef, error) {
	if len(args) < 4 {
		return nil, errors.New(
			"ERR Invalid number of arguments specified for command",
		)
	}

	keys := zsetMPopKeys(args)
	if len(keys) == 0 {
		return nil, errors.New("ERR syntax error")
	}

	refs := make([]commandKeyRef, 0, len(keys))

	for _, key := range keys {
		refs = append(
			refs,
			commandKeyRef{
				value: []byte(key),
				flags: []string{
					"RW",
					"access",
					"delete",
				},
			},
		)
	}

	return refs, nil
}

func commandBZMPopKeys(
	args [][]byte,
) ([]commandKeyRef, error) {
	if len(args) < 5 {
		return nil, errors.New(
			"ERR Invalid number of arguments specified for command",
		)
	}

	numKeys, err := strconv.Atoi(string(args[2]))
	if err != nil || numKeys <= 0 {
		return nil, errors.New(
			"ERR numkeys should be greater than 0",
		)
	}

	firstKey := 3
	lastKey := firstKey + numKeys

	if lastKey > len(args) {
		return nil, errors.New("ERR syntax error")
	}

	refs := make([]commandKeyRef, 0, numKeys)

	for _, key := range args[firstKey:lastKey] {
		refs = append(
			refs,
			commandKeyRef{
				value: key,
				flags: []string{
					"RW",
					"access",
					"delete",
				},
			},
		)
	}

	return refs, nil
}

func commandXReadKeys(
	args [][]byte,
) ([]commandKeyRef, error) {
	request, err := parseXRead(args)
	if err != nil {
		return nil, err
	}

	refs := make([]commandKeyRef, 0, len(request.keys))

	for _, key := range request.keys {
		refs = append(
			refs,
			commandKeyRef{
				value: []byte(key),
				flags: []string{"RO", "access"},
			},
		)
	}

	return refs, nil
}

func commandXReadGroupKeys(
	args [][]byte,
) ([]commandKeyRef, error) {
	request, err := parseXReadGroup(args)
	if err != nil {
		return nil, err
	}

	refs := make([]commandKeyRef, 0, len(request.keys))

	for _, key := range request.keys {
		refs = append(
			refs,
			commandKeyRef{
				value: []byte(key),
				flags: []string{
					"RO",
					"access",
				},
			},
		)
	}

	return refs, nil
}

func commandKeyCountKeys(
	name string,
	args [][]byte,
) ([]commandKeyRef, error) {
	if len(args) < 3 {
		return nil, errors.New(
			"ERR Invalid number of arguments specified for command",
		)
	}

	count, err := strconv.ParseInt(
		string(args[2]),
		10,
		64,
	)
	if err != nil {
		return nil, errors.New(
			"ERR value is not an integer or out of range",
		)
	}

	if count < 0 {
		return nil, errors.New(
			"ERR Number of keys can't be negative",
		)
	}

	if count > int64(len(args)-3) {
		return nil, errors.New(
			"ERR Number of keys can't be greater than number of args",
		)
	}

	if count == 0 {
		return nil, nil
	}

	flags := []string{"RW", "access", "update"}

	switch name {
	case "EVAL_RO", "EVALSHA_RO", "FCALL_RO":
		flags = []string{"RO", "access"}
	}

	refs := make([]commandKeyRef, 0, int(count))

	for index := 0; index < int(count); index++ {
		refs = append(
			refs,
			commandKeyRef{
				value: args[3+index],
				flags: append(
					[]string(nil),
					flags...,
				),
			},
		)
	}

	return refs, nil
}

func commandKeyFlags(
	name string,
	index int,
	write bool,
) []string {
	switch name {
	case "GET", "MGET", "EXISTS", "TYPE",
		"TTL", "PTTL", "EXPIRETIME", "PEXPIRETIME",
		"STRLEN", "GETRANGE", "GETBIT":
		return []string{"RO", "access"}

	case "SET":
		return []string{"OW", "update"}

	case "DEL", "UNLINK":
		return []string{"RM", "delete"}

	case "RENAME", "RENAMENX":
		if index == 1 {
			return []string{"RW", "access", "delete"}
		}

		return []string{"OW", "update"}

	case "MSET", "MSETNX":
		return []string{"OW", "update"}
	}

	if write {
		return []string{"RW", "update"}
	}

	return []string{"RO", "access"}
}

func commandGetKeysReply(refs []commandKeyRef) []byte {
	items := make([][]byte, 0, len(refs))

	for _, ref := range refs {
		items = append(
			items,
			formatBulkString(ref.value),
		)
	}

	return array(items...)
}

func commandGetKeysAndFlagsReply(
	refs []commandKeyRef,
) []byte {
	items := make([][]byte, 0, len(refs))

	for _, ref := range refs {
		flags := make([][]byte, 0, len(ref.flags))

		for _, flag := range ref.flags {
			// Redis renders these as RESP simple strings.
			flags = append(
				flags,
				[]byte("+"+flag+"\r\n"),
			)
		}

		items = append(
			items,
			array(
				formatBulkString(ref.value),
				array(flags...),
			),
		)
	}

	return array(items...)
}

func commandRESPString(value string) []byte {
	return []byte("+" + value + "\r\n")
}

func commandRESPStrings(values []string) []byte {
	items := make([][]byte, 0, len(values))

	for _, value := range values {
		items = append(
			items,
			commandRESPString(value),
		)
	}

	return array(items...)
}

func commandBulkStrings(values []string) []byte {
	items := make([][]byte, 0, len(values))

	for _, value := range values {
		items = append(
			items,
			formatBulkString([]byte(value)),
		)
	}

	return array(items...)
}

func commandInfoArity(info commandInfo) int {
	if info.max == info.min {
		return info.min
	}

	return -info.min
}

func commandInfoFlags(
	name string,
	info commandInfo,
) []string {
	switch name {
	case "GET":
		return []string{"readonly", "fast"}

	case "SET":
		return []string{"write", "denyoom"}

	case "COPY":
		return []string{"write", "denyoom"}

	case "SORT":
		return []string{"write", "denyoom", "movablekeys"}

	case "SORT_RO":
		return []string{"readonly", "movablekeys"}

	case "XADD":
		return []string{"write", "denyoom", "fast"}

	case "PFADD":
		return []string{"write", "denyoom", "fast"}

	case "GEOADD":
		return []string{"write", "denyoom"}

	case "EVAL", "EVALSHA",
		"EVAL_RO", "EVALSHA_RO",
		"FCALL", "FCALL_RO":
		return []string{
			"noscript",
			"stale",
			"skip_monitor",
			"no_mandatory_keys",
			"movablekeys",
		}
	}

	if info.write {
		return []string{"write"}
	}

	return []string{"readonly"}
}

func commandInfoACL(
	name string,
	info commandInfo,
) []string {
	switch name {
	case "GET":
		return []string{
			"@read",
			"@string",
			"@fast",
		}

	case "SET":
		return []string{
			"@write",
			"@string",
			"@slow",
		}

	case "DEL":
		return []string{
			"@keyspace",
			"@write",
			"@slow",
		}

	case "COPY":
		return []string{
			"@keyspace",
			"@write",
			"@slow",
		}

	case "EVAL", "EVALSHA",
		"EVAL_RO", "EVALSHA_RO",
		"FCALL", "FCALL_RO":
		return []string{
			"@slow",
			"@scripting",
		}

	case "SORT":
		return []string{
			"@write",
			"@set",
			"@sortedset",
			"@list",
			"@slow",
			"@dangerous",
		}

	case "SORT_RO":
		return []string{
			"@read",
			"@set",
			"@sortedset",
			"@list",
			"@slow",
			"@dangerous",
		}

	case "XADD":
		return []string{
			"@write",
			"@stream",
			"@fast",
		}

	case "PFADD":
		return []string{
			"@write",
			"@hyperloglog",
			"@fast",
		}

	case "GEOADD":
		return []string{
			"@write",
			"@geo",
			"@slow",
		}
	}

	if info.write {
		return []string{"@write"}
	}

	return []string{"@read"}
}

func commandInfoTips(name string) []string {
	switch name {
	case "DEL":
		return []string{
			"request_policy:multi_shard",
			"response_policy:agg_sum",
		}

	case "XADD":
		return []string{
			"nondeterministic_output",
		}
	}

	return nil
}

func commandKeySpec(
	flags []string,
	first int,
	last int,
	step int,
) []byte {
	return commandKeySpecWithNotes(
		flags,
		first,
		last,
		step,
		"",
	)
}

func commandKeySpecWithNotes(
	flags []string,
	first int,
	last int,
	step int,
	notes string,
) []byte {
	parts := make([][]byte, 0, 8)

	if notes != "" {
		parts = append(
			parts,
			formatBulkString([]byte("notes")),
			formatBulkString([]byte(notes)),
		)
	}

	parts = append(
		parts,
		formatBulkString([]byte("flags")),
		commandRESPStrings(flags),

		formatBulkString([]byte("begin_search")),
		array(
			formatBulkString([]byte("type")),
			formatBulkString([]byte("index")),
			formatBulkString([]byte("spec")),
			array(
				formatBulkString([]byte("index")),
				integer(int64(first)),
			),
		),

		formatBulkString([]byte("find_keys")),
		array(
			formatBulkString([]byte("type")),
			formatBulkString([]byte("range")),
			formatBulkString([]byte("spec")),
			array(
				formatBulkString([]byte("lastkey")),
				integer(int64(last)),
				formatBulkString([]byte("keystep")),
				integer(int64(step)),
				formatBulkString([]byte("limit")),
				integer(0),
			),
		),
	)

	return array(parts...)
}

func commandKeyNumSpec(
	flags []string,
	beginIndex int,
) []byte {
	return commandKeyNumSpecWithNotes(
		flags,
		beginIndex,
		"",
	)
}

func commandKeyNumSpecWithNotes(
	flags []string,
	beginIndex int,
	notes string,
) []byte {
	parts := make([][]byte, 0, 8)

	if notes != "" {
		parts = append(
			parts,
			formatBulkString([]byte("notes")),
			formatBulkString([]byte(notes)),
		)
	}

	parts = append(
		parts,
		formatBulkString([]byte("flags")),
		commandRESPStrings(flags),

		formatBulkString([]byte("begin_search")),
		array(
			formatBulkString([]byte("type")),
			formatBulkString([]byte("index")),
			formatBulkString([]byte("spec")),
			array(
				formatBulkString([]byte("index")),
				integer(int64(beginIndex)),
			),
		),

		formatBulkString([]byte("find_keys")),
		array(
			formatBulkString([]byte("type")),
			formatBulkString([]byte("keynum")),
			formatBulkString([]byte("spec")),
			array(
				formatBulkString([]byte("keynumidx")),
				integer(0),
				formatBulkString([]byte("firstkey")),
				integer(1),
				formatBulkString([]byte("keystep")),
				integer(1),
			),
		),
	)

	return array(parts...)
}

func commandUnknownKeySpec(
	flags []string,
	notes string,
) []byte {
	parts := make([][]byte, 0, 10)

	if notes != "" {
		parts = append(
			parts,
			formatBulkString([]byte("notes")),
			formatBulkString([]byte(notes)),
		)
	}

	parts = append(
		parts,
		formatBulkString([]byte("flags")),
		commandRESPStrings(flags),

		formatBulkString([]byte("begin_search")),
		array(
			formatBulkString([]byte("type")),
			formatBulkString([]byte("unknown")),
			formatBulkString([]byte("spec")),
			array(),
		),

		formatBulkString([]byte("find_keys")),
		array(
			formatBulkString([]byte("type")),
			formatBulkString([]byte("unknown")),
			formatBulkString([]byte("spec")),
			array(),
		),
	)

	return array(parts...)
}

func commandInfoKeySpecs(
	name string,
	info commandInfo,
) []byte {
	switch name {
	case "EVAL", "EVALSHA", "FCALL":
		return array(
			commandKeyNumSpecWithNotes(
				[]string{
					"RW",
					"access",
					"update",
				},
				2,
				"We cannot tell how the keys will be used so we assume the worst, RW and UPDATE",
			),
		)

	case "EVAL_RO", "EVALSHA_RO", "FCALL_RO":
		return array(
			commandKeyNumSpec(
				[]string{
					"RO",
					"access",
				},
				2,
			),
		)

	case "COPY":
		return array(
			commandKeySpec(
				[]string{
					"RO",
					"access",
				},
				1,
				0,
				1,
			),
			commandKeySpec(
				[]string{
					"OW",
					"update",
				},
				2,
				0,
				1,
			),
		)

	case "SORT":
		return array(
			commandKeySpec(
				[]string{
					"RO",
					"access",
				},
				1,
				0,
				1,
			),

			commandUnknownKeySpec(
				[]string{
					"RO",
					"access",
				},
				"For the optional BY/GET keyword. It is marked 'unknown' because the key names derive from the content of the key we sort",
			),

			commandUnknownKeySpec(
				[]string{
					"OW",
					"update",
				},
				"For the optional STORE keyword. It is marked 'unknown' because the keyword can appear anywhere in the argument array",
			),
		)
	}

	if info.first == 0 || info.step <= 0 {
		return array()
	}

	flags := []string{"RO", "access"}
	notes := ""

	switch name {
	case "SET":
		flags = []string{
			"RW",
			"access",
			"update",
			"variable_flags",
		}
		notes = "RW and ACCESS due to the optional `GET` argument"

	case "DEL", "UNLINK":
		flags = []string{"RM", "delete"}

	case "XADD":
		flags = []string{"RW", "update"}
		notes = "UPDATE instead of INSERT because of the optional trimming feature"

	case "PFADD":
		flags = []string{"RW", "insert"}

	case "RENAME", "RENAMENX":
		// The legacy first/last/step metadata cannot express
		// source/destination flag differences accurately.
		// GETKEYSANDFLAGS already does, so do not publish a
		// misleading generic spec here yet.
		return array()

	default:
		if info.write {
			flags = []string{"RW", "update"}
		}
	}

	last := info.last

	if last > 0 {
		// Redis range key specs express the last key relative
		// to the first key rather than as an absolute argv
		// position.
		last -= info.first
	}

	return array(
		commandKeySpecWithNotes(
			flags,
			info.first,
			last,
			info.step,
			notes,
		),
	)
}

func commandInfoSubcommands(name string) []byte {
	// Parent command subcommand metadata will be added as the next
	// slice. Returning an empty array is preferable to advertising
	// Redis subcommands that SnugKV does not implement.
	return array()
}

func commandInfoReply(name string) []byte {
	info, ok := commandTable[name]
	if !ok {
		return nullBulk()
	}

	return array(
		formatBulkString(
			[]byte(strings.ToLower(name)),
		),

		integer(
			int64(commandInfoArity(info)),
		),

		commandRESPStrings(
			commandInfoFlags(name, info),
		),

		integer(int64(info.first)),
		integer(int64(info.last)),
		integer(int64(info.step)),

		commandRESPStrings(
			commandInfoACL(name, info),
		),

		commandBulkStrings(
			commandInfoTips(name),
		),

		commandInfoKeySpecs(name, info),

		commandInfoSubcommands(name),
	)
}
