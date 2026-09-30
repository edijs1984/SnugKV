package main

import "testing"

func TestRedisClusterKeySlotCompatibility(t *testing.T) {
	tests := []struct {
		key  string
		slot int
	}{
		{key: "foo", slot: 12182},
		{key: "bar", slot: 5061},
		{key: "{user1000}.following", slot: 3443},
		{key: "{user1000}.followers", slot: 3443},
	}
	for _, tt := range tests {
		if got := slotForKey(tt.key); got != tt.slot {
			t.Fatalf("slotForKey(%q) = %d, want %d", tt.key, got, tt.slot)
		}
	}
}

func TestStaticOwnerRanges(t *testing.T) {
	nodes := []string{"n0", "n1", "n2"}
	tests := []struct {
		slot int
		want string
	}{
		{0, "n0"},
		{5460, "n0"},
		{5461, "n1"},
		{10922, "n1"},
		{10923, "n2"},
		{16383, "n2"},
	}
	for _, tt := range tests {
		if got := staticOwner(tt.slot, nodes); got != tt.want {
			t.Fatalf("staticOwner(%d) = %q, want %q", tt.slot, got, tt.want)
		}
	}
}
