package server

// registerReleaseStructuredCommandMetadata closes the first-release introspection
// gap for subcommands whose behavior already existed but was not advertised by
// COMMAND INFO/inventory metadata.
func init() {
	registerReleaseCommandParent("ACL", []string{"ACL|CAT", "ACL|DELUSER", "ACL|DRYRUN", "ACL|GENPASS", "ACL|GETUSER", "ACL|HELP", "ACL|LIST", "ACL|LOAD", "ACL|LOG", "ACL|SAVE", "ACL|SETUSER", "ACL|USERS", "ACL|WHOAMI"})
	registerReleaseCommandParent("CLUSTER", []string{"CLUSTER|ADDSLOTS", "CLUSTER|COUNTKEYSINSLOT", "CLUSTER|DELSLOTS", "CLUSTER|FLUSHSLOTS", "CLUSTER|GETKEYSINSLOT", "CLUSTER|INFO", "CLUSTER|KEYSLOT", "CLUSTER|MYID", "CLUSTER|NODES", "CLUSTER|SETSLOT", "CLUSTER|SHARDS", "CLUSTER|SLOTS"})
	commandParentMetadataTable["COMMAND"] = appendCommandParentSubcommands(commandParentMetadataTable["COMMAND"], "COMMAND|LIST")
	registerReleaseCommandParent("MEMORY", []string{"MEMORY|USAGE"})
	registerReleaseCommandParent("OBJECT", []string{"OBJECT|ENCODING", "OBJECT|HELP", "OBJECT|REFCOUNT"})
	registerReleaseCommandParent("PUBSUB", []string{"PUBSUB|CHANNELS", "PUBSUB|HELP", "PUBSUB|NUMPAT", "PUBSUB|NUMSUB", "PUBSUB|SHARDCHANNELS", "PUBSUB|SHARDNUMSUB"})
	registerReleaseCommandParent("SLOWLOG", []string{"SLOWLOG|GET", "SLOWLOG|HELP", "SLOWLOG|LEN", "SLOWLOG|RESET"})
	registerReleaseCommandParent("XGROUP", []string{"XGROUP|CREATE", "XGROUP|CREATECONSUMER", "XGROUP|DELCONSUMER", "XGROUP|DESTROY", "XGROUP|SETID"})
	registerReleaseCommandParent("XINFO", []string{"XINFO|CONSUMERS", "XINFO|GROUPS", "XINFO|HELP", "XINFO|STREAM"})
	commandLeafMetadataTable["ACL|CAT"] = commandLeafMetadata{arity: -2, flags: []string{"noscript", "loading", "stale"}, acl: []string{"@slow"}, tips: []string{}}
	commandLeafMetadataTable["ACL|DELUSER"] = commandLeafMetadata{arity: -3, flags: []string{"admin", "noscript", "loading", "stale"}, acl: []string{"@admin", "@slow", "@dangerous"}, tips: []string{"request_policy:all_nodes", "response_policy:all_succeeded"}}
	commandLeafMetadataTable["ACL|DRYRUN"] = commandLeafMetadata{arity: -4, flags: []string{"admin", "noscript", "loading", "stale"}, acl: []string{"@admin", "@slow", "@dangerous"}, tips: []string{}}
	commandLeafMetadataTable["ACL|GENPASS"] = commandLeafMetadata{arity: -2, flags: []string{"noscript", "loading", "stale"}, acl: []string{"@slow"}, tips: []string{}}
	commandLeafMetadataTable["ACL|GETUSER"] = commandLeafMetadata{arity: 3, flags: []string{"admin", "noscript", "loading", "stale"}, acl: []string{"@admin", "@slow", "@dangerous"}, tips: []string{}}
	commandLeafMetadataTable["ACL|HELP"] = commandLeafMetadata{arity: 2, flags: []string{"loading", "stale"}, acl: []string{"@slow"}, tips: []string{}}
	commandLeafMetadataTable["ACL|LIST"] = commandLeafMetadata{arity: 2, flags: []string{"admin", "noscript", "loading", "stale"}, acl: []string{"@admin", "@slow", "@dangerous"}, tips: []string{}}
	commandLeafMetadataTable["ACL|LOAD"] = commandLeafMetadata{arity: 2, flags: []string{"admin", "noscript", "loading", "stale"}, acl: []string{"@admin", "@slow", "@dangerous"}, tips: []string{}}
	commandLeafMetadataTable["ACL|LOG"] = commandLeafMetadata{arity: -2, flags: []string{"admin", "noscript", "loading", "stale"}, acl: []string{"@admin", "@slow", "@dangerous"}, tips: []string{}}
	commandLeafMetadataTable["ACL|SAVE"] = commandLeafMetadata{arity: 2, flags: []string{"admin", "noscript", "loading", "stale"}, acl: []string{"@admin", "@slow", "@dangerous"}, tips: []string{"request_policy:all_nodes", "response_policy:all_succeeded"}}
	commandLeafMetadataTable["ACL|SETUSER"] = commandLeafMetadata{arity: -3, flags: []string{"admin", "noscript", "loading", "stale"}, acl: []string{"@admin", "@slow", "@dangerous"}, tips: []string{"request_policy:all_nodes", "response_policy:all_succeeded"}}
	commandLeafMetadataTable["ACL|USERS"] = commandLeafMetadata{arity: 2, flags: []string{"admin", "noscript", "loading", "stale"}, acl: []string{"@admin", "@slow", "@dangerous"}, tips: []string{}}
	commandLeafMetadataTable["ACL|WHOAMI"] = commandLeafMetadata{arity: 2, flags: []string{"noscript", "loading", "stale"}, acl: []string{"@slow"}, tips: []string{}}
	commandLeafMetadataTable["CLUSTER|ADDSLOTS"] = commandLeafMetadata{arity: -3, flags: []string{"admin", "stale", "no_async_loading"}, acl: []string{"@admin", "@slow", "@dangerous"}, tips: []string{}}
	commandLeafMetadataTable["CLUSTER|COUNTKEYSINSLOT"] = commandLeafMetadata{arity: 3, flags: []string{"stale"}, acl: []string{"@slow"}, tips: []string{}}
	commandLeafMetadataTable["CLUSTER|DELSLOTS"] = commandLeafMetadata{arity: -3, flags: []string{"admin", "stale", "no_async_loading"}, acl: []string{"@admin", "@slow", "@dangerous"}, tips: []string{}}
	commandLeafMetadataTable["CLUSTER|FLUSHSLOTS"] = commandLeafMetadata{arity: 2, flags: []string{"admin", "stale", "no_async_loading"}, acl: []string{"@admin", "@slow", "@dangerous"}, tips: []string{}}
	commandLeafMetadataTable["CLUSTER|GETKEYSINSLOT"] = commandLeafMetadata{arity: 4, flags: []string{"stale"}, acl: []string{"@slow"}, tips: []string{"nondeterministic_output"}}
	commandLeafMetadataTable["CLUSTER|INFO"] = commandLeafMetadata{arity: 2, flags: []string{"stale"}, acl: []string{"@slow"}, tips: []string{"nondeterministic_output"}}
	commandLeafMetadataTable["CLUSTER|KEYSLOT"] = commandLeafMetadata{arity: 3, flags: []string{"stale"}, acl: []string{"@slow"}, tips: []string{}}
	commandLeafMetadataTable["CLUSTER|MYID"] = commandLeafMetadata{arity: 2, flags: []string{"stale"}, acl: []string{"@slow"}, tips: []string{}}
	commandLeafMetadataTable["CLUSTER|NODES"] = commandLeafMetadata{arity: 2, flags: []string{"stale"}, acl: []string{"@slow"}, tips: []string{"nondeterministic_output"}}
	commandLeafMetadataTable["CLUSTER|SETSLOT"] = commandLeafMetadata{arity: -4, flags: []string{"admin", "stale", "no_async_loading"}, acl: []string{"@admin", "@slow", "@dangerous"}, tips: []string{}}
	commandLeafMetadataTable["CLUSTER|SHARDS"] = commandLeafMetadata{arity: 2, flags: []string{"loading", "stale"}, acl: []string{"@slow"}, tips: []string{"nondeterministic_output"}}
	commandLeafMetadataTable["CLUSTER|SLOTS"] = commandLeafMetadata{arity: 2, flags: []string{"loading", "stale"}, acl: []string{"@slow"}, tips: []string{"nondeterministic_output"}}
	commandLeafMetadataTable["COMMAND|LIST"] = commandLeafMetadata{arity: -2, flags: []string{"loading", "stale"}, acl: []string{"@slow", "@connection"}, tips: []string{"nondeterministic_output_order"}}
	commandLeafMetadataTable["MEMORY|USAGE"] = commandLeafMetadata{arity: -3, flags: []string{"readonly"}, acl: []string{"@read", "@slow"}, tips: []string{}}
	commandLeafMetadataTable["OBJECT|ENCODING"] = commandLeafMetadata{arity: 3, flags: []string{"readonly"}, acl: []string{"@keyspace", "@read", "@slow"}, tips: []string{"nondeterministic_output"}}
	commandLeafMetadataTable["OBJECT|HELP"] = commandLeafMetadata{arity: 2, flags: []string{"loading", "stale"}, acl: []string{"@keyspace", "@slow"}, tips: []string{}}
	commandLeafMetadataTable["OBJECT|REFCOUNT"] = commandLeafMetadata{arity: 3, flags: []string{"readonly"}, acl: []string{"@keyspace", "@read", "@slow"}, tips: []string{"nondeterministic_output"}}
	commandLeafMetadataTable["PUBSUB|CHANNELS"] = commandLeafMetadata{arity: -2, flags: []string{"pubsub", "loading", "stale"}, acl: []string{"@pubsub", "@slow"}, tips: []string{}}
	commandLeafMetadataTable["PUBSUB|HELP"] = commandLeafMetadata{arity: 2, flags: []string{"loading", "stale"}, acl: []string{"@slow"}, tips: []string{}}
	commandLeafMetadataTable["PUBSUB|NUMPAT"] = commandLeafMetadata{arity: 2, flags: []string{"pubsub", "loading", "stale"}, acl: []string{"@pubsub", "@slow"}, tips: []string{}}
	commandLeafMetadataTable["PUBSUB|NUMSUB"] = commandLeafMetadata{arity: -2, flags: []string{"pubsub", "loading", "stale"}, acl: []string{"@pubsub", "@slow"}, tips: []string{}}
	commandLeafMetadataTable["PUBSUB|SHARDCHANNELS"] = commandLeafMetadata{arity: -2, flags: []string{"pubsub", "loading", "stale"}, acl: []string{"@pubsub", "@slow"}, tips: []string{}}
	commandLeafMetadataTable["PUBSUB|SHARDNUMSUB"] = commandLeafMetadata{arity: -2, flags: []string{"pubsub", "loading", "stale"}, acl: []string{"@pubsub", "@slow"}, tips: []string{}}
	commandLeafMetadataTable["SLOWLOG|GET"] = commandLeafMetadata{arity: -2, flags: []string{"admin", "loading", "stale"}, acl: []string{"@admin", "@slow", "@dangerous"}, tips: []string{"request_policy:all_nodes", "nondeterministic_output"}}
	commandLeafMetadataTable["SLOWLOG|HELP"] = commandLeafMetadata{arity: 2, flags: []string{"loading", "stale"}, acl: []string{"@slow"}, tips: []string{}}
	commandLeafMetadataTable["SLOWLOG|LEN"] = commandLeafMetadata{arity: 2, flags: []string{"admin", "loading", "stale"}, acl: []string{"@admin", "@slow", "@dangerous"}, tips: []string{"request_policy:all_nodes", "response_policy:agg_sum", "nondeterministic_output"}}
	commandLeafMetadataTable["SLOWLOG|RESET"] = commandLeafMetadata{arity: 2, flags: []string{"admin", "loading", "stale"}, acl: []string{"@admin", "@slow", "@dangerous"}, tips: []string{"request_policy:all_nodes", "response_policy:all_succeeded"}}
	commandLeafMetadataTable["XGROUP|CREATE"] = commandLeafMetadata{arity: -5, flags: []string{"write", "denyoom"}, acl: []string{"@write", "@stream", "@slow"}, tips: []string{}}
	commandLeafMetadataTable["XGROUP|CREATECONSUMER"] = commandLeafMetadata{arity: 5, flags: []string{"write", "denyoom"}, acl: []string{"@write", "@stream", "@slow"}, tips: []string{}}
	commandLeafMetadataTable["XGROUP|DELCONSUMER"] = commandLeafMetadata{arity: 5, flags: []string{"write"}, acl: []string{"@write", "@stream", "@slow"}, tips: []string{}}
	commandLeafMetadataTable["XGROUP|DESTROY"] = commandLeafMetadata{arity: 4, flags: []string{"write"}, acl: []string{"@write", "@stream", "@slow"}, tips: []string{}}
	commandLeafMetadataTable["XGROUP|SETID"] = commandLeafMetadata{arity: -5, flags: []string{"write"}, acl: []string{"@write", "@stream", "@slow"}, tips: []string{}}
	commandLeafMetadataTable["XINFO|CONSUMERS"] = commandLeafMetadata{arity: 4, flags: []string{"readonly"}, acl: []string{"@read", "@stream", "@slow"}, tips: []string{"nondeterministic_output"}}
	commandLeafMetadataTable["XINFO|GROUPS"] = commandLeafMetadata{arity: 3, flags: []string{"readonly"}, acl: []string{"@read", "@stream", "@slow"}, tips: []string{}}
	commandLeafMetadataTable["XINFO|HELP"] = commandLeafMetadata{arity: 2, flags: []string{"loading", "stale"}, acl: []string{"@stream", "@slow"}, tips: []string{}}
	commandLeafMetadataTable["XINFO|STREAM"] = commandLeafMetadata{arity: -3, flags: []string{"readonly"}, acl: []string{"@read", "@stream", "@slow"}, tips: []string{}}
}

func registerReleaseCommandParent(name string, subcommands []string) {
	info, ok := commandTable[name]
	if !ok { return }
	commandParentMetadataTable[name] = commandParentMetadata{
		arity: commandInfoArity(info),
		flags: commandInfoFlags(name, info),
		acl: commandInfoACL(name, info),
		subcommands: append([]string(nil), subcommands...),
	}
}

func appendCommandParentSubcommands(meta commandParentMetadata, subcommands ...string) commandParentMetadata {
	seen := make(map[string]struct{}, len(meta.subcommands)+len(subcommands))
	for _, name := range meta.subcommands { seen[name]=struct{}{} }
	for _, name := range subcommands {
		if _, ok := seen[name]; ok { continue }
		meta.subcommands = append(meta.subcommands, name)
		seen[name]=struct{}{}
	}
	return meta
}
