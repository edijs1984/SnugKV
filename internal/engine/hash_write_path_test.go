package engine

import (
	"fmt"
	"testing"
)

// Single-field HSET across the packed/indexed boundary must behave like a map,
// including overwrites, and idle compaction must not change the content.
func TestHashSingleFieldWritesMatchMapAcrossIndexedBoundary(t *testing.T) {
	s := New()
	want := map[string]string{}
	const key = "h"
	for i := 0; i < 400; i++ {
		field := fmt.Sprintf("field-%03d", (i*37)%250)
		value := fmt.Sprintf("value-%d", i)
		added, err := s.HashSet(key, [][]byte{[]byte(field)}, [][]byte{[]byte(value)})
		if err != nil {
			t.Fatal(err)
		}
		_, existed := want[field]
		if (added == 1) == existed {
			t.Fatalf("write %d field %s: added=%d existed=%v", i, field, added, existed)
		}
		want[field] = value
	}

	check := func(label string) {
		pairs, err := s.HashGetAll(key)
		if err != nil {
			t.Fatal(label, err)
		}
		if len(pairs) != len(want) {
			t.Fatalf("%s: %d fields, want %d", label, len(pairs), len(want))
		}
		for _, p := range pairs {
			if want[string(p.Field)] != string(p.Value) {
				t.Fatalf("%s: field %s = %q, want %q", label, p.Field, p.Value, want[string(p.Field)])
			}
		}
	}
	check("after writes")
	s.Compact(1 << 30)
	check("after compact")
}
