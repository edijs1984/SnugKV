package server

import (
	"errors"
	"sort"
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

	case "GEORADIUS", "GEORADIUSBYMEMBER":
		return commandLegacyGeoRadiusKeys(
			name,
			args,
		)

	case "MIGRATE":
		_, options, err := migrateSourceKeys(args)
		if err != nil {
			return nil, err
		}
		refs := make([]commandKeyRef, 0, len(options.keys))
		for _, key := range options.keys {
			refs = append(refs, commandKeyRef{
				value: key,
				flags: []string{"RW", "access", "delete"},
			})
		}
		return refs, nil

	case "CMS.MERGE":
		if len(args) < 4 {
			return nil, errors.New("ERR Invalid number of arguments specified for command")
		}
		n, err := strconv.Atoi(string(args[2]))
		if err != nil || n <= 0 || 3+n > len(args) {
			return nil, errors.New("ERR Invalid number of arguments specified for command")
		}
		refs := []commandKeyRef{{value: args[1], flags: []string{"RW", "update"}}}
		for i := 0; i < n; i++ {
			refs = append(refs, commandKeyRef{value: args[3+i], flags: []string{"RO", "access"}})
		}
		return refs, nil

	case "TDIGEST.MERGE":
		if len(args) < 4 {
			return nil, errors.New("ERR Invalid number of arguments specified for command")
		}
		n, err := strconv.Atoi(string(args[2]))
		if err != nil || n <= 0 || 3+n > len(args) {
			return nil, errors.New("ERR Invalid number of arguments specified for command")
		}
		refs := []commandKeyRef{{value: args[1], flags: []string{"RW", "update"}}}
		for i := 0; i < n; i++ {
			refs = append(refs, commandKeyRef{value: args[3+i], flags: []string{"RO", "access"}})
		}
		return refs, nil

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

func commandDenyOOM(name string) bool {
	switch strings.ToUpper(name) {
	case "SET", "SETNX", "SETEX", "PSETEX",
		"GETSET", "APPEND",
		"INCR", "INCRBY", "DECR", "DECRBY", "INCRBYFLOAT",
		"MSET", "MSETNX",
		"HSET",
		"SADD",
		"LPUSH", "RPUSH",
		"ZADD",
		"XADD",
		"PFADD",
		"BF.RESERVE", "BF.ADD", "BF.MADD", "BF.INSERT",
		"CF.RESERVE", "CF.ADD", "CF.ADDNX", "CF.DEL", "CF.INSERT", "CF.INSERTNX",
		"CMS.INITBYDIM", "CMS.INITBYPROB", "CMS.INCRBY", "CMS.MERGE",
		"TOPK.RESERVE", "TOPK.ADD", "TOPK.INCRBY",
		"TDIGEST.CREATE", "TDIGEST.ADD", "TDIGEST.RESET", "TDIGEST.MERGE",
		"GEOADD",
		"BITOP",
		"COPY",
		"SORT",
		"ZUNIONSTORE", "ZINTERSTORE", "ZDIFFSTORE":
		return true
	default:
		return false
	}
}

func commandInfoFlags(
	name string,
	info commandInfo,
) []string {
	switch name {
	case "GET":
		return []string{"readonly", "fast"}

	case "TOUCH":
		return []string{"readonly", "fast"}

	case "EXPIRE", "PERSIST", "HDEL", "SREM", "LPOP", "ZREM", "XDEL":
		return []string{"write", "fast"}

	case "HSET", "SADD", "LPUSH", "ZADD":
		return []string{"write", "denyoom", "fast"}

	case "SET", "COPY":
		return []string{"write", "denyoom"}

	case "BITOP":
		return []string{"write", "denyoom"}

	case "SORT":
		return []string{"write", "denyoom", "movablekeys"}

	case "SORT_RO":
		return []string{"readonly", "movablekeys"}

	case "ZUNION", "ZINTER", "ZDIFF", "ZINTERCARD":
		return []string{"readonly", "movablekeys"}

	case "ZUNIONSTORE", "ZINTERSTORE", "ZDIFFSTORE":
		return []string{"write", "denyoom", "movablekeys"}

	case "ZMPOP":
		return []string{"write", "movablekeys"}

	case "BZMPOP":
		return []string{"write", "blocking", "movablekeys"}

	case "XADD":
		return []string{"write", "denyoom", "fast"}

	case "XREAD":
		return []string{"readonly", "blocking", "movablekeys"}

	case "XREADGROUP":
		return []string{"write", "blocking", "movablekeys"}

	case "PFADD":
		return []string{"write", "denyoom", "fast"}

	case "BF.RESERVE", "BF.ADD", "BF.MADD", "BF.INSERT":
		return []string{"write", "denyoom", "fast"}

	case "CF.RESERVE", "CF.ADD", "CF.ADDNX", "CF.DEL", "CF.INSERT", "CF.INSERTNX":
		return []string{"write", "denyoom", "fast"}

	case "CMS.INITBYDIM", "CMS.INITBYPROB":
		return []string{"write", "denyoom", "fast"}

	case "CMS.INCRBY", "CMS.MERGE":
		return []string{"write", "denyoom"}

	case "TOPK.RESERVE":
		return []string{"write", "denyoom", "fast"}

	case "TOPK.ADD", "TOPK.INCRBY":
		return []string{"write", "denyoom"}

	case "TDIGEST.CREATE", "TDIGEST.RESET":
		return []string{"write", "denyoom", "fast"}

	case "TDIGEST.ADD", "TDIGEST.MERGE":
		return []string{"write", "denyoom"}

	case "GEOADD":
		return []string{"write", "denyoom"}

	case "EVAL", "EVALSHA", "FCALL":
		return []string{
			"noscript",
			"stale",
			"skip_monitor",
			"no_mandatory_keys",
			"movablekeys",
		}

	case "EVAL_RO", "EVALSHA_RO", "FCALL_RO":
		return []string{
			"readonly",
			"noscript",
			"stale",
			"skip_monitor",
			"no_mandatory_keys",
			"movablekeys",
		}
	}

	if info.write {
		if commandDenyOOM(name) {
			return []string{"write", "denyoom"}
		}
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

	case "MIGRATE":
		return []string{
			"@keyspace",
			"@dangerous",
		}

	case "BITOP":
		return []string{
			"@write",
			"@bitmap",
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

	case "ZUNION", "ZINTER", "ZDIFF", "ZINTERCARD":
		return []string{
			"@read",
			"@sortedset",
			"@slow",
		}

	case "ZUNIONSTORE", "ZINTERSTORE", "ZDIFFSTORE",
		"ZMPOP":
		return []string{
			"@write",
			"@sortedset",
			"@slow",
		}

	case "BZMPOP":
		return []string{
			"@write",
			"@sortedset",
			"@slow",
			"@blocking",
		}

	case "XADD":
		return []string{
			"@write",
			"@stream",
			"@fast",
		}

	case "XREAD":
		return []string{
			"@read",
			"@stream",
			"@slow",
			"@blocking",
		}

	case "XREADGROUP":
		return []string{
			"@write",
			"@stream",
			"@slow",
			"@blocking",
		}

	case "PFADD":
		return []string{
			"@write",
			"@hyperloglog",
			"@fast",
		}

	case "BF.RESERVE", "BF.ADD", "BF.MADD", "BF.INSERT":
		return []string{
			"@write",
			"@bloom",
			"@fast",
		}

	case "BF.EXISTS", "BF.MEXISTS", "BF.CARD", "BF.INFO":
		return []string{
			"@read",
			"@bloom",
			"@fast",
		}

	case "CF.RESERVE", "CF.ADD", "CF.ADDNX", "CF.DEL", "CF.INSERT", "CF.INSERTNX":
		return []string{
			"@write",
			"@cuckoo",
			"@fast",
		}

	case "CF.EXISTS", "CF.MEXISTS", "CF.COUNT", "CF.INFO":
		return []string{
			"@read",
			"@cuckoo",
			"@fast",
		}

	case "CMS.INITBYDIM", "CMS.INITBYPROB":
		return []string{
			"@write",
			"@cms",
			"@fast",
		}

	case "CMS.INCRBY", "CMS.MERGE":
		return []string{
			"@write",
			"@cms",
			"@slow",
		}

	case "CMS.QUERY":
		return []string{
			"@read",
			"@cms",
			"@slow",
		}

	case "CMS.INFO":
		return []string{
			"@read",
			"@cms",
			"@fast",
		}

	case "TOPK.RESERVE":
		return []string{
			"@write",
			"@topk",
			"@fast",
		}

	case "TOPK.ADD", "TOPK.INCRBY":
		return []string{
			"@write",
			"@topk",
			"@slow",
		}

	case "TOPK.QUERY", "TOPK.COUNT", "TOPK.LIST":
		return []string{
			"@read",
			"@topk",
			"@slow",
		}

	case "TOPK.INFO":
		return []string{
			"@read",
			"@topk",
			"@fast",
		}

	case "TDIGEST.CREATE", "TDIGEST.RESET":
		return []string{
			"@write",
			"@tdigest",
			"@fast",
		}

	case "TDIGEST.ADD", "TDIGEST.MERGE":
		return []string{
			"@write",
			"@tdigest",
			"@slow",
		}

	case "TDIGEST.MIN", "TDIGEST.MAX", "TDIGEST.INFO":
		return []string{
			"@read",
			"@tdigest",
			"@fast",
		}

	case "TDIGEST.QUANTILE", "TDIGEST.CDF", "TDIGEST.RANK", "TDIGEST.REVRANK",
		"TDIGEST.BYRANK", "TDIGEST.BYREVRANK", "TDIGEST.TRIMMED_MEAN":
		return []string{
			"@read",
			"@tdigest",
			"@slow",
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
	case "MIGRATE":
		return []string{
			"nondeterministic_output",
		}

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

func commandKeywordRangeSpec(
	flags []string,
	notes string,
	keyword string,
	startFrom int,
	lastKey int,
	keyStep int,
	limit int,
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
			formatBulkString([]byte("keyword")),
			formatBulkString([]byte("spec")),
			array(
				formatBulkString([]byte("keyword")),
				formatBulkString([]byte(keyword)),
				formatBulkString([]byte("startfrom")),
				integer(int64(startFrom)),
			),
		),

		formatBulkString([]byte("find_keys")),
		array(
			formatBulkString([]byte("type")),
			formatBulkString([]byte("range")),
			formatBulkString([]byte("spec")),
			array(
				formatBulkString([]byte("lastkey")),
				integer(int64(lastKey)),
				formatBulkString([]byte("keystep")),
				integer(int64(keyStep)),
				formatBulkString([]byte("limit")),
				integer(int64(limit)),
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
	case "EVAL", "FCALL":
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

	case "EVALSHA":
		return array(
			commandKeyNumSpec(
				[]string{
					"RW",
					"access",
					"update",
				},
				2,
			),
		)

	case "EVAL_RO", "FCALL_RO":
		return array(
			commandKeyNumSpecWithNotes(
				[]string{
					"RO",
					"access",
				},
				2,
				"We cannot tell how the keys will be used so we assume the worst, RO and ACCESS",
			),
		)

	case "EVALSHA_RO":
		return array(
			commandKeyNumSpec(
				[]string{
					"RO",
					"access",
				},
				2,
			),
		)

	case "MIGRATE":
		return array(
			commandKeySpec(
				[]string{
					"RW",
					"access",
					"delete",
				},
				3,
				0,
				1,
			),
			commandKeywordRangeSpec(
				[]string{
					"RW",
					"access",
					"delete",
					"incomplete",
				},
				"KEYS form uses every argument after the KEYS keyword",
				"KEYS",
				-2,
				-1,
				1,
				0,
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

	case "BITOP":
		return array(
			commandKeySpec(
				[]string{
					"OW",
					"update",
				},
				2,
				0,
				1,
			),
			commandKeySpec(
				[]string{
					"RO",
					"access",
				},
				3,
				-1,
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

	case "SORT_RO":
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
		)

	case "ZUNION", "ZINTER", "ZDIFF", "ZINTERCARD":
		return array(
			commandKeyNumSpec(
				[]string{
					"RO",
					"access",
				},
				1,
			),
		)

	case "ZUNIONSTORE", "ZINTERSTORE", "ZDIFFSTORE":
		return array(
			commandKeySpec(
				[]string{
					"OW",
					"update",
				},
				1,
				0,
				1,
			),

			commandKeyNumSpec(
				[]string{
					"RO",
					"access",
				},
				2,
			),
		)

	case "ZMPOP":
		return array(
			commandKeyNumSpec(
				[]string{
					"RW",
					"access",
					"delete",
				},
				1,
			),
		)

	case "BZMPOP":
		return array(
			commandKeyNumSpec(
				[]string{
					"RW",
					"access",
					"delete",
				},
				2,
			),
		)

	case "XREAD":
		return array(
			commandKeywordRangeSpec(
				[]string{
					"RO",
					"access",
					"incomplete",
				},
				"Incomplete because a stream key named STREAMS (or options before it) can shift the STREAMS keyword; fall back to xreadGetKeys",
				"STREAMS",
				1,
				-1,
				1,
				2,
			),
		)

	case "XREADGROUP":
		return array(
			commandKeywordRangeSpec(
				[]string{
					"RO",
					"access",
					"incomplete",
				},
				"Incomplete because a consumer/group named STREAMS (or options before GROUP) can shift the STREAMS keyword; fall back to xreadGetKeys",
				"STREAMS",
				4,
				-1,
				1,
				2,
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
		return array()

	default:
		if info.write {
			flags = []string{"RW", "update"}
		}
	}

	last := info.last

	if last > 0 {
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

func commandInfoLegacyReply(name string) []byte {
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

type commandDocArgument struct {
	name         string
	displayText  string
	argType      string
	token        string
	since        string
	optional     bool
	multiple     bool
	keySpecIndex int
	hasKeySpec   bool
	arguments    []commandDocArgument
}

type commandDoc struct {
	summary    string
	since      string
	group      string
	complexity string
	history    [][2]string
	arguments  []commandDocArgument
}

var commandDocs = map[string]commandDoc{
	"GET": {
		summary:    "Returns the string value of a key.",
		since:      "1.0.0",
		group:      "string",
		complexity: "O(1)",
		arguments: []commandDocArgument{
			{
				name:         "key",
				displayText:  "key",
				argType:      "key",
				keySpecIndex: 0,
				hasKeySpec:   true,
			},
		},
	},

	"SET": {
		summary:    "Sets the string value of a key, ignoring its type. The key is created if it doesn't exist.",
		since:      "1.0.0",
		group:      "string",
		complexity: "O(1)",
		history: [][2]string{
			{"2.6.12", "Added the `EX`, `PX`, `NX` and `XX` options."},
			{"6.0.0", "Added the `KEEPTTL` option."},
			{"6.2.0", "Added the `GET`, `EXAT` and `PXAT` option."},
			{"7.0.0", "Allowed the `NX` and `GET` options to be used together."},
		},
		arguments: []commandDocArgument{
			{
				name:         "key",
				displayText:  "key",
				argType:      "key",
				keySpecIndex: 0,
				hasKeySpec:   true,
			},
			{
				name:        "value",
				displayText: "value",
				argType:     "string",
			},
			{
				name:     "condition",
				argType:  "oneof",
				since:    "2.6.12",
				optional: true,
				arguments: []commandDocArgument{
					{
						name:        "nx",
						displayText: "nx",
						argType:     "pure-token",
						token:       "NX",
					},
					{
						name:        "xx",
						displayText: "xx",
						argType:     "pure-token",
						token:       "XX",
					},
				},
			},
			{
				name:        "get",
				displayText: "get",
				argType:     "pure-token",
				token:       "GET",
				since:       "6.2.0",
				optional:    true,
			},
			{
				name:     "expiration",
				argType:  "oneof",
				optional: true,
				arguments: []commandDocArgument{
					{
						name:        "seconds",
						displayText: "seconds",
						argType:     "integer",
						token:       "EX",
						since:       "2.6.12",
					},
					{
						name:        "milliseconds",
						displayText: "milliseconds",
						argType:     "integer",
						token:       "PX",
						since:       "2.6.12",
					},
					{
						name:        "unix-time-seconds",
						displayText: "unix-time-seconds",
						argType:     "unix-time",
						token:       "EXAT",
						since:       "6.2.0",
					},
					{
						name:        "unix-time-milliseconds",
						displayText: "unix-time-milliseconds",
						argType:     "unix-time",
						token:       "PXAT",
						since:       "6.2.0",
					},
					{
						name:        "keepttl",
						displayText: "keepttl",
						argType:     "pure-token",
						token:       "KEEPTTL",
						since:       "6.0.0",
					},
				},
			},
		},
	},

	"DEL": {
		summary:    "Deletes one or more keys.",
		since:      "1.0.0",
		group:      "generic",
		complexity: "O(N) where N is the number of keys that will be removed. When a key to remove holds a value other than a string, the individual complexity for this key is O(M) where M is the number of elements in the list, set, sorted set or hash. Removing a single key that holds a string value is O(1).",
		arguments: []commandDocArgument{
			{
				name:         "key",
				displayText:  "key",
				argType:      "key",
				multiple:     true,
				keySpecIndex: 0,
				hasKeySpec:   true,
			},
		},
	},

	"MIGRATE": {
		summary:    "Atomically transfers a key from one Redis instance to another.",
		since:      "2.6.0",
		group:      "generic",
		complexity: "DUMP+DEL on the source, RESTORE on the target, plus O(N) network transfer.",
		history: [][2]string{
			{"3.0.0", "Added the COPY and REPLACE options."},
			{"3.0.6", "Added the KEYS option."},
			{"4.0.7", "Added the AUTH option."},
			{"6.0.0", "Added the AUTH2 option."},
		},
	},

	"COPY": {
		summary:    "Copies the value of a key to a new key.",
		since:      "6.2.0",
		group:      "generic",
		complexity: "O(N) worst case for collections, where N is the number of nested items. O(1) for string values.",
		arguments: []commandDocArgument{
			{
				name:         "source",
				displayText:  "source",
				argType:      "key",
				keySpecIndex: 0,
				hasKeySpec:   true,
			},
			{
				name:         "destination",
				displayText:  "destination",
				argType:      "key",
				keySpecIndex: 1,
				hasKeySpec:   true,
			},
			{
				name:        "destination-db",
				displayText: "destination-db",
				argType:     "integer",
				token:       "DB",
				optional:    true,
			},
			{
				name:        "replace",
				displayText: "replace",
				argType:     "pure-token",
				token:       "REPLACE",
				optional:    true,
			},
		},
	},

	"EVAL": {
		summary:    "Executes a server-side Lua script.",
		since:      "2.6.0",
		group:      "scripting",
		complexity: "Depends on the script that is executed.",
		arguments: []commandDocArgument{
			{
				name:        "script",
				displayText: "script",
				argType:     "string",
			},
			{
				name:        "numkeys",
				displayText: "numkeys",
				argType:     "integer",
			},
			{
				name:         "key",
				displayText:  "key",
				argType:      "key",
				optional:     true,
				multiple:     true,
				keySpecIndex: 0,
				hasKeySpec:   true,
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

	"EVAL_RO": {
		summary:    "Executes a read-only Lua script on the server.",
		since:      "7.0.0",
		group:      "scripting",
		complexity: "Depends on the script that is executed.",
	},

	"FCALL": {
		summary:    "Invokes a function.",
		since:      "7.0.0",
		group:      "scripting",
		complexity: "Depends on the function that is executed.",
		arguments: []commandDocArgument{
			{
				name:        "function",
				displayText: "function",
				argType:     "string",
			},
			{
				name:        "numkeys",
				displayText: "numkeys",
				argType:     "integer",
			},
			{
				name:         "key",
				displayText:  "key",
				argType:      "key",
				optional:     true,
				multiple:     true,
				keySpecIndex: 0,
				hasKeySpec:   true,
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

	"FCALL_RO": {
		summary:    "Calls a read-only server-side function.",
		since:      "7.0.0",
		group:      "scripting",
		complexity: "Depends on the function that is executed.",
	},

	"SORT": {
		summary:    "Sorts the elements contained in a list, set, or sorted set.",
		since:      "1.0.0",
		group:      "generic",
		complexity: "O(N+M*log(M)) where N is the number of elements and M is the number returned.",
	},

	"SORT_RO": {
		summary:    "Returns sorted elements without allowing STORE.",
		since:      "7.0.0",
		group:      "generic",
		complexity: "O(N+M*log(M)) where N is the number of elements and M is the number returned.",
	},

	"XADD": {
		summary:    "Appends a new message to a stream. Creates the key if it doesn't exist.",
		since:      "5.0.0",
		group:      "stream",
		complexity: "O(1) when adding a new entry, O(N) when trimming where N being the number of entries evicted.",
		history: [][2]string{
			{"6.2.0", "Added the `NOMKSTREAM` option, `MINID` trimming strategy and the `LIMIT` option."},
			{"7.0.0", "Added support for the `<ms>-*` explicit ID form."},
		},
		arguments: []commandDocArgument{
			{
				name:         "key",
				displayText:  "key",
				argType:      "key",
				keySpecIndex: 0,
				hasKeySpec:   true,
			},
			{
				name:        "nomkstream",
				displayText: "nomkstream",
				argType:     "pure-token",
				token:       "NOMKSTREAM",
				since:       "6.2.0",
				optional:    true,
			},
			{
				name:     "trim",
				argType:  "block",
				optional: true,
				arguments: []commandDocArgument{
					{
						name:    "strategy",
						argType: "oneof",
						arguments: []commandDocArgument{
							{
								name:        "maxlen",
								displayText: "maxlen",
								argType:     "pure-token",
								token:       "MAXLEN",
							},
							{
								name:        "minid",
								displayText: "minid",
								argType:     "pure-token",
								token:       "MINID",
								since:       "6.2.0",
							},
						},
					},
					{
						name:     "operator",
						argType:  "oneof",
						optional: true,
						arguments: []commandDocArgument{
							{
								name:        "equal",
								displayText: "equal",
								argType:     "pure-token",
								token:       "=",
							},
							{
								name:        "approximately",
								displayText: "approximately",
								argType:     "pure-token",
								token:       "~",
							},
						},
					},
					{
						name:        "threshold",
						displayText: "threshold",
						argType:     "string",
					},
					{
						name:        "count",
						displayText: "count",
						argType:     "integer",
						token:       "LIMIT",
						since:       "6.2.0",
						optional:    true,
					},
				},
			},
			{
				name:    "id-selector",
				argType: "oneof",
				arguments: []commandDocArgument{
					{
						name:        "auto-id",
						displayText: "auto-id",
						argType:     "pure-token",
						token:       "*",
					},
					{
						name:        "id",
						displayText: "id",
						argType:     "string",
					},
				},
			},
			{
				name:     "data",
				argType:  "block",
				multiple: true,
				arguments: []commandDocArgument{
					{
						name:        "field",
						displayText: "field",
						argType:     "string",
					},
					{
						name:        "value",
						displayText: "value",
						argType:     "string",
					},
				},
			},
		},
	},

	"XREAD": {
		summary:    "Reads entries from one or more streams.",
		since:      "5.0.0",
		group:      "stream",
		complexity: "O(N) for the number of entries returned.",
	},

	"XREADGROUP": {
		summary:    "Reads stream entries using a consumer group.",
		since:      "5.0.0",
		group:      "stream",
		complexity: "O(N) for the number of entries returned.",
	},

	"PFADD": {
		summary:    "Adds elements to a HyperLogLog key. Creates the key if it doesn't exist.",
		since:      "2.8.9",
		group:      "hyperloglog",
		complexity: "O(1) to add every element.",
		arguments: []commandDocArgument{
			{
				name:         "key",
				displayText:  "key",
				argType:      "key",
				keySpecIndex: 0,
				hasKeySpec:   true,
			},
			{
				name:        "element",
				displayText: "element",
				argType:     "string",
				optional:    true,
				multiple:    true,
			},
		},
	},

	"GEOADD": {
		summary:    "Adds one or more members to a geospatial index. The key is created if it doesn't exist.",
		since:      "3.2.0",
		group:      "geo",
		complexity: "O(log(N)) for each item added, where N is the number of elements in the sorted set.",
		history: [][2]string{
			{"6.2.0", "Added the `CH`, `NX` and `XX` options."},
		},
		arguments: []commandDocArgument{
			{
				name:         "key",
				displayText:  "key",
				argType:      "key",
				keySpecIndex: 0,
				hasKeySpec:   true,
			},
			{
				name:     "condition",
				argType:  "oneof",
				since:    "6.2.0",
				optional: true,
				arguments: []commandDocArgument{
					{
						name:        "nx",
						displayText: "nx",
						argType:     "pure-token",
						token:       "NX",
					},
					{
						name:        "xx",
						displayText: "xx",
						argType:     "pure-token",
						token:       "XX",
					},
				},
			},
			{
				name:        "change",
				displayText: "change",
				argType:     "pure-token",
				token:       "CH",
				since:       "6.2.0",
				optional:    true,
			},
			{
				name:     "data",
				argType:  "block",
				multiple: true,
				arguments: []commandDocArgument{
					{
						name:        "longitude",
						displayText: "longitude",
						argType:     "double",
					},
					{
						name:        "latitude",
						displayText: "latitude",
						argType:     "double",
					},
					{
						name:        "member",
						displayText: "member",
						argType:     "string",
					},
				},
			},
		},
	},

	"CLIENT": {
		summary:    "Manages client connections and connection metadata.",
		since:      "2.4.0",
		group:      "connection",
		complexity: "Depends on the selected subcommand.",
	},

	"FUNCTION": {
		summary:    "Manages server-side function libraries.",
		since:      "7.0.0",
		group:      "scripting",
		complexity: "Depends on the selected subcommand.",
	},

	"SCRIPT": {
		summary:    "Manages the Lua script cache.",
		since:      "2.6.0",
		group:      "scripting",
		complexity: "Depends on the selected subcommand.",
	},
}

func commandDocArgumentReply(
	arg commandDocArgument,
) []byte {
	parts := make([][]byte, 0, 20)

	parts = append(
		parts,
		formatBulkString([]byte("name")),
		formatBulkString([]byte(arg.name)),
		formatBulkString([]byte("type")),
		formatBulkString([]byte(arg.argType)),
	)

	if arg.displayText != "" {
		parts = append(
			parts,
			formatBulkString([]byte("display_text")),
			formatBulkString([]byte(arg.displayText)),
		)
	}

	if arg.token != "" {
		parts = append(
			parts,
			formatBulkString([]byte("token")),
			formatBulkString([]byte(arg.token)),
		)
	}

	if arg.since != "" {
		parts = append(
			parts,
			formatBulkString([]byte("since")),
			formatBulkString([]byte(arg.since)),
		)
	}

	if arg.hasKeySpec {
		parts = append(
			parts,
			formatBulkString([]byte("key_spec_index")),
			integer(int64(arg.keySpecIndex)),
		)
	}

	flags := make([][]byte, 0, 2)

	if arg.optional {
		flags = append(
			flags,
			commandRESPString("optional"),
		)
	}

	if arg.multiple {
		flags = append(
			flags,
			commandRESPString("multiple"),
		)
	}

	if len(flags) > 0 {
		parts = append(
			parts,
			formatBulkString([]byte("flags")),
			array(flags...),
		)
	}

	if len(arg.arguments) > 0 {
		children := make(
			[][]byte,
			0,
			len(arg.arguments),
		)

		for _, child := range arg.arguments {
			children = append(
				children,
				commandDocArgumentReply(child),
			)
		}

		parts = append(
			parts,
			formatBulkString([]byte("arguments")),
			array(children...),
		)
	}

	return array(parts...)
}

func commandDocReply(doc commandDoc) []byte {
	parts := make([][]byte, 0, 12)

	parts = append(
		parts,
		formatBulkString([]byte("summary")),
		formatBulkString([]byte(doc.summary)),

		formatBulkString([]byte("since")),
		formatBulkString([]byte(doc.since)),

		formatBulkString([]byte("group")),
		formatBulkString([]byte(doc.group)),

		formatBulkString([]byte("complexity")),
		formatBulkString([]byte(doc.complexity)),
	)

	if len(doc.history) > 0 {
		history := make([][]byte, 0, len(doc.history))

		for _, item := range doc.history {
			history = append(
				history,
				array(
					formatBulkString([]byte(item[0])),
					formatBulkString([]byte(item[1])),
				),
			)
		}

		parts = append(
			parts,
			formatBulkString([]byte("history")),
			array(history...),
		)
	}

	if len(doc.arguments) > 0 {
		args := make([][]byte, 0, len(doc.arguments))

		for _, arg := range doc.arguments {
			args = append(
				args,
				commandDocArgumentReply(arg),
			)
		}

		parts = append(
			parts,
			formatBulkString([]byte("arguments")),
			array(args...),
		)
	}

	return array(parts...)
}

func commandDocsLegacyReply(names [][]byte) []byte {
	requested := make([]string, 0)

	if len(names) == 0 {
		requested = make(
			[]string,
			0,
			len(commandDocs),
		)

		for name := range commandDocs {
			if _, ok := commandTable[name]; ok {
				requested = append(
					requested,
					name,
				)
			}
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

			if _, ok := commandTable[name]; !ok {
				continue
			}

			if _, ok := commandDocs[name]; !ok {
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
		doc := commandDocs[name]

		parts = append(
			parts,
			formatBulkString(
				[]byte(strings.ToLower(name)),
			),
			commandDocReply(doc),
		)
	}

	return array(parts...)
}

func commandLegacyGeoRadiusKeys(
	name string,
	args [][]byte,
) ([]commandKeyRef, error) {
	byMember := name == "GEORADIUSBYMEMBER"

	parsed, err := parseLegacyGeoRadiusSpec(
		args,
		byMember,
	)
	if err != nil {
		return nil, err
	}

	refs := []commandKeyRef{
		{
			value: args[1],
			flags: []string{"RO", "access"},
		},
	}

	if parsed.store {
		refs = append(
			refs,
			commandKeyRef{
				value: []byte(parsed.destination),
				flags: []string{"OW", "update"},
			},
		)
	}

	return refs, nil
}
