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
