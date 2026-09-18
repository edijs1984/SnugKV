package server

import (
	"fmt"
	"sort"
	"strings"
)

var aclCategoryNames = []string{
	"keyspace",
	"read",
	"write",
	"set",
	"sortedset",
	"list",
	"hash",
	"string",
	"bitmap",
	"hyperloglog",
	"geo",
	"stream",
	"pubsub",
	"admin",
	"fast",
	"slow",
	"blocking",
	"dangerous",
	"connection",
	"transaction",
	"scripting",
	"bloom",
	"cuckoo",
	"cms",
	"topk",
	"tdigest",
	"search",
	"timeseries",
	"json",
}

func aclCategoryReply(category string) ([]byte, error) {
	if category == "" {
		items := make([][]byte, 0, len(aclCategoryNames))

		for _, name := range aclCategoryNames {
			items = append(
				items,
				formatBulkString([]byte(name)),
			)
		}

		return array(items...), nil
	}

	category = strings.ToLower(category)

	redisCommands, ok := redisACLCategoryCommands[category]
	if !ok {
		return nil, fmt.Errorf(
			"ERR Unknown category '%s'",
			category,
		)
	}

	commands := make([]string, 0)

	for command := range commandTable {
		name := strings.ToLower(command)

		if _, ok := redisCommands[name]; !ok {
			continue
		}

		commands = append(commands, name)
	}

	sort.Strings(commands)

	items := make([][]byte, 0, len(commands))

	for _, command := range commands {
		items = append(
			items,
			formatBulkString([]byte(command)),
		)
	}

	return array(items...), nil
}
