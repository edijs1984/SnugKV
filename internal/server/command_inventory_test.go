package server

import (
	"strings"
	"testing"
)

func TestCommandInventoryMatchesCommandCount(t *testing.T) {
	entries := CommandInventorySnapshot()
	top := 0
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if _, ok := seen[entry.Name]; ok {
			t.Fatalf("duplicate inventory entry %q", entry.Name)
		}
		seen[entry.Name] = struct{}{}

		if entry.Kind == "command" {
			top++
			if entry.Parent != "" {
				t.Fatalf("top-level command %q unexpectedly has parent %q", entry.Name, entry.Parent)
			}
			continue
		}

		if entry.Kind != "subcommand" {
			t.Fatalf("entry %q has unknown kind %q", entry.Name, entry.Kind)
		}
		if entry.Parent == "" || !strings.Contains(entry.Name, "|") {
			t.Fatalf("subcommand %q has invalid parent metadata", entry.Name)
		}
	}

	if top != TopLevelCommandCount() {
		t.Fatalf("inventory top-level count=%d command count=%d", top, TopLevelCommandCount())
	}
	if top != len(commandTable) {
		t.Fatalf("inventory top-level count=%d commandTable=%d", top, len(commandTable))
	}
}

func TestCommandInventoryDeterministicOrdering(t *testing.T) {
	entries := CommandInventorySnapshot()
	lastTop := ""
	lastSub := ""
	inSubs := false
	for _, entry := range entries {
		switch entry.Kind {
		case "command":
			if inSubs {
				t.Fatalf("top-level command %q appears after subcommands", entry.Name)
			}
			if lastTop != "" && entry.Name <= lastTop {
				t.Fatalf("top-level inventory not sorted: %q after %q", entry.Name, lastTop)
			}
			lastTop = entry.Name
		case "subcommand":
			inSubs = true
			if lastSub != "" && entry.Name <= lastSub {
				t.Fatalf("subcommand inventory not sorted: %q after %q", entry.Name, lastSub)
			}
			lastSub = entry.Name
		}
	}
}
