package server

import (
	"sort"
	"strings"
)

// CommandInventoryEntry is a stable, read-only view of the command metadata used
// by COMMAND and the execution path. It exists so release tooling can generate
// compatibility inventories without duplicating command lists by hand.
type CommandInventoryEntry struct {
	Name           string   `json:"name"`
	Parent         string   `json:"parent,omitempty"`
	Kind           string   `json:"kind"`
	Arity          int      `json:"arity"`
	MinArgs        int      `json:"min_args,omitempty"`
	MaxArgs        int      `json:"max_args,omitempty"`
	Flags          []string `json:"flags,omitempty"`
	ACLCategories  []string `json:"acl_categories,omitempty"`
	KeyMode        string   `json:"key_mode"`
	Write          bool     `json:"write"`
	ClusterRouting string   `json:"cluster_routing"`
	MetadataSource string   `json:"metadata_source"`
}

// CommandInventorySnapshot returns a deterministic snapshot of SnugKV's
// registered top-level commands plus structured subcommands.
//
// Top-level entries are exactly the entries counted by COMMAND COUNT.
// Subcommands are returned separately with Kind="subcommand" and therefore do
// not inflate TopLevelCommandCount.
func CommandInventorySnapshot() []CommandInventoryEntry {
	topNames := make([]string, 0, len(commandTable))
	for name := range commandTable {
		topNames = append(topNames, name)
	}
	sort.Strings(topNames)

	entries := make([]CommandInventoryEntry, 0, len(topNames)+len(commandLeafMetadataTable))
	for _, name := range topNames {
		info := commandTable[name]
		keyMode := commandInventoryKeyMode(name, info)
		entries = append(entries, CommandInventoryEntry{
			Name:           name,
			Kind:           "command",
			Arity:          commandInfoArity(info),
			MinArgs:        info.min,
			MaxArgs:        info.max,
			Flags:          append([]string(nil), commandInfoFlags(name, info)...),
			ACLCategories:  append([]string(nil), commandInfoACL(name, info)...),
			KeyMode:        keyMode,
			Write:          info.write,
			ClusterRouting: commandInventoryClusterRouting(name, info, keyMode),
			MetadataSource: "internal/server/commandTable",
		})
	}

	leafNames := make([]string, 0, len(commandLeafMetadataTable))
	for name := range commandLeafMetadataTable {
		leafNames = append(leafNames, name)
	}
	sort.Strings(leafNames)

	for _, name := range leafNames {
		meta := commandLeafMetadataTable[name]
		parent := name
		if before, _, ok := strings.Cut(name, "|"); ok {
			parent = before
		}
		entries = append(entries, CommandInventoryEntry{
			Name:           name,
			Parent:         parent,
			Kind:           "subcommand",
			Arity:          meta.arity,
			Flags:          append([]string(nil), meta.flags...),
			ACLCategories:  append([]string(nil), meta.acl...),
			KeyMode:        "none",
			Write:          containsInventoryFlag(meta.flags, "write"),
			ClusterRouting: "no-key-routing",
			MetadataSource: "internal/server/commandLeafMetadataTable",
		})
	}

	return entries
}

// TopLevelCommandCount returns the exact number used by COMMAND COUNT.
func TopLevelCommandCount() int {
	return len(commandTable)
}

func commandInventoryKeyMode(name string, info commandInfo) string {
	flags := commandInfoFlags(name, info)
	if containsInventoryFlag(flags, "movablekeys") || name == "MIGRATE" {
		return "dynamic"
	}
	if info.first == 0 || info.step <= 0 {
		return "none"
	}
	return "static"
}

func commandInventoryClusterRouting(name string, info commandInfo, keyMode string) string {
	switch keyMode {
	case "none":
		return "no-key-routing"
	case "dynamic":
		return "same-slot-dynamic"
	}

	if info.first > 0 && info.last == info.first {
		return "single-key"
	}
	return "same-slot-static"
}

func containsInventoryFlag(flags []string, want string) bool {
	for _, flag := range flags {
		if strings.EqualFold(flag, want) {
			return true
		}
	}
	return false
}
