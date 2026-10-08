package engine

import (
	"bytes"
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

// TestTinyFixedSetContainsMatchesReference checks the prefix-skipping lookup
// against a plain scan for random fixed-width sets, including heavy shared
// prefixes, single-byte alphabets and targets that fall between, before and
// after the stored members.
func TestTinyFixedSetContainsMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for iter := 0; iter < 4000; iter++ {
		width := 1 + rng.Intn(24)
		alphabet := 1 + rng.Intn(5)
		if iter%7 == 0 {
			alphabet = 256
		}
		n := 2 + rng.Intn(tinySetMaxMembers-1)
		seen := map[string]bool{}
		var members [][]byte
		for tries := 0; len(members) < n && tries < n*20; tries++ {
			m := make([]byte, width)
			for i := range m {
				m[i] = byte(rng.Intn(alphabet))
				if alphabet < 256 {
					m[i] += 'a'
				}
			}
			if !seen[string(m)] {
				seen[string(m)] = true
				members = append(members, m)
			}
		}
		if len(members) < 2 {
			continue
		}
		sort.Slice(members, func(i, j int) bool { return bytes.Compare(members[i], members[j]) < 0 })
		data, ok := encodeTinyFixedSet(members)
		if !ok {
			t.Fatalf("iter %d: encodeTinyFixedSet refused fixed-width members", iter)
		}
		for probe := 0; probe < 60; probe++ {
			var target []byte
			if probe%3 == 0 {
				target = members[rng.Intn(len(members))]
			} else {
				target = make([]byte, width)
				for i := range target {
					target[i] = byte(rng.Intn(alphabet))
					if alphabet < 256 {
						target[i] += 'a'
					}
				}
			}
			want := seen[string(target)]
			got, err := tinyFixedSetContains(data, target)
			if err != nil {
				t.Fatalf("iter %d: unexpected error: %v", iter, err)
			}
			if got != want {
				t.Fatalf("iter %d: contains(%x)=%v want %v (members=%d width=%d)", iter, target, got, want, len(members), width)
			}
		}
		if got, err := tinyFixedSetContains(data, make([]byte, width+1)); err != nil || got {
			t.Fatalf("iter %d: wrong-width target must be absent, got %v err %v", iter, got, err)
		}
	}
}

// TestFixedWidthSetStaysTinyUntilCap walks a fixed-width set through the tiny
// layout up to tinySetMaxMembers, checks every observable operation against a
// map model at each size, then pushes it past the cap and mixes in members of
// other widths.
func TestFixedWidthSetStaysTinyUntilCap(t *testing.T) {
	s := New()
	model := map[string]bool{}
	check := func(label string) {
		t.Helper()
		n, err := s.SetLen("k")
		if err != nil || int(n) != len(model) {
			t.Fatalf("%s: len=%d err=%v want %d", label, n, err, len(model))
		}
		members, err := s.SetMembers("k")
		if err != nil || len(members) != len(model) {
			t.Fatalf("%s: members=%d err=%v want %d", label, len(members), err, len(model))
		}
		for _, m := range members {
			if !model[string(m)] {
				t.Fatalf("%s: unexpected member %q", label, m)
			}
		}
		for m := range model {
			ok, err := s.SetContains("k", []byte(m))
			if err != nil || !ok {
				t.Fatalf("%s: contains(%q)=%v err=%v", label, m, ok, err)
			}
		}
		if ok, _ := s.SetContains("k", []byte("zz-absent-member-0000")); ok {
			t.Fatalf("%s: absent member reported present", label)
		}
	}
	add := func(m string, wantAdded int64) {
		t.Helper()
		got, err := s.SetAdd("k", [][]byte{[]byte(m)})
		if err != nil || got != wantAdded {
			t.Fatalf("add %q: got %d err %v want %d", m, got, err, wantAdded)
		}
		model[m] = true
	}
	for i := 0; i < tinySetMaxMembers+20; i++ {
		m := fmt.Sprintf("member-%08d", (i*7919)%100003)
		add(m, 1)
		add(m, 0) // re-adding is a no-op
		if i%9 == 0 || i == tinySetMaxMembers-1 || i == tinySetMaxMembers {
			check(fmt.Sprintf("fixed width, %d members", len(model)))
		}
	}
	for _, m := range []string{"x", "a-much-longer-member-than-the-others", "member-short"} {
		add(m, 1)
	}
	check("mixed widths past the cap")
	removed := 0
	for m := range model {
		if removed == len(model)-5 {
			break
		}
		if n, err := s.SetRemove("k", [][]byte{[]byte(m)}); err != nil || n != 1 {
			t.Fatalf("remove %q: %d %v", m, n, err)
		}
		delete(model, m)
		removed++
	}
	check("after removals")
}
