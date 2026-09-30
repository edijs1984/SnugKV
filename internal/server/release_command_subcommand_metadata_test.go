package server

import "testing"

func TestReleaseImplementedSubcommandsAreStructuredMetadata(t *testing.T) {
	expected := []string{
		"ACL|CAT",
		"ACL|DELUSER",
		"ACL|DRYRUN",
		"ACL|GENPASS",
		"ACL|GETUSER",
		"ACL|HELP",
		"ACL|LIST",
		"ACL|LOAD",
		"ACL|LOG",
		"ACL|SAVE",
		"ACL|SETUSER",
		"ACL|USERS",
		"ACL|WHOAMI",
		"CLUSTER|ADDSLOTS",
		"CLUSTER|COUNTKEYSINSLOT",
		"CLUSTER|DELSLOTS",
		"CLUSTER|FLUSHSLOTS",
		"CLUSTER|GETKEYSINSLOT",
		"CLUSTER|INFO",
		"CLUSTER|KEYSLOT",
		"CLUSTER|MYID",
		"CLUSTER|NODES",
		"CLUSTER|SETSLOT",
		"CLUSTER|SHARDS",
		"CLUSTER|SLOTS",
		"COMMAND|LIST",
		"MEMORY|USAGE",
		"OBJECT|ENCODING",
		"OBJECT|HELP",
		"OBJECT|REFCOUNT",
		"PUBSUB|CHANNELS",
		"PUBSUB|HELP",
		"PUBSUB|NUMPAT",
		"PUBSUB|NUMSUB",
		"PUBSUB|SHARDCHANNELS",
		"PUBSUB|SHARDNUMSUB",
		"SLOWLOG|GET",
		"SLOWLOG|HELP",
		"SLOWLOG|LEN",
		"SLOWLOG|RESET",
		"XGROUP|CREATE",
		"XGROUP|CREATECONSUMER",
		"XGROUP|DELCONSUMER",
		"XGROUP|DESTROY",
		"XGROUP|SETID",
		"XINFO|CONSUMERS",
		"XINFO|GROUPS",
		"XINFO|HELP",
		"XINFO|STREAM",
	}
	if len(expected) != 49 {
		t.Fatalf("fixture count=%d want=49", len(expected))
	}

	inventory := CommandInventorySnapshot()
	seen := make(map[string]CommandInventoryEntry, len(inventory))
	for _, entry := range inventory {
		seen[entry.Name] = entry
	}

	for _, name := range expected {
		meta, ok := commandLeafMetadataTable[name]
		if !ok {
			t.Errorf("missing leaf metadata for %s", name)
			continue
		}
		entry, ok := seen[name]
		if !ok {
			t.Errorf("inventory missing %s", name)
			continue
		}
		if entry.Kind != "subcommand" {
			t.Errorf("%s kind=%q want subcommand", name, entry.Kind)
		}
		if entry.Arity != meta.arity {
			t.Errorf("%s arity=%d want=%d", name, entry.Arity, meta.arity)
		}
	}
}

func TestReleaseStructuredParentsAdvertiseTheirLeaves(t *testing.T) {
	for parent, meta := range commandParentMetadataTable {
		for _, child := range meta.subcommands {
			if _, ok := commandLeafMetadataTable[child]; !ok {
				t.Fatalf("%s advertises missing child metadata %s", parent, child)
			}
		}
	}
}
