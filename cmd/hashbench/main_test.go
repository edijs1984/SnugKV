package main

import (
	"bytes"
	"testing"
)

func TestFieldsForKeyModes(t *testing.T) {
	shared, _ := dataset(4, 32)

	gotShared := fieldsForKey("shared", 99, 4, shared, 80)
	for i := range shared {
		if !bytes.Equal(gotShared[i], shared[i]) {
			t.Fatalf("shared field %d changed", i)
		}
	}

	first := fieldsForKey("unique", 1, 4, shared, 80)
	second := fieldsForKey("unique", 2, 4, shared, 80)
	for i := range first {
		if len(first[i]) != len(shared[i]) {
			t.Fatalf("unique field %d length = %d, want %d", i, len(first[i]), len(shared[i]))
		}
		if bytes.Equal(first[i], second[i]) {
			t.Fatalf("unique field %d repeated across keys", i)
		}
	}

	mixedShared := fieldsForKey("mixed", 79, 4, shared, 80)
	mixedUnique := fieldsForKey("mixed", 80, 4, shared, 80)
	if !bytes.Equal(mixedShared[0], shared[0]) {
		t.Fatal("mixed shared partition did not reuse shared fields")
	}
	if bytes.Equal(mixedUnique[0], shared[0]) {
		t.Fatal("mixed unique partition reused shared fields")
	}
}

func TestUniqueFieldNamesDoNotCollide(t *testing.T) {
	seen := make(map[string]struct{})
	for key := 0; key < 1000; key++ {
		fields := uniqueFields(key, 8)
		for _, field := range fields {
			name := string(field)
			if _, exists := seen[name]; exists {
				t.Fatalf("duplicate unique field %q", name)
			}
			seen[name] = struct{}{}
		}
	}
}
