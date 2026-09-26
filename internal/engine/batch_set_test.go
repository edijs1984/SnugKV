package engine

import (
	"fmt"
	"testing"
)

func TestSetPlainBatchFreshAcrossConfigurableShards(t *testing.T) {
	store, err := NewWithShards(1024)
	if err != nil {
		t.Fatal(err)
	}

	const n = 256
	keys := make([][]byte, n)
	values := make([][]byte, n)
	for i := 0; i < n; i++ {
		keys[i] = []byte(fmt.Sprintf("batch:%03d", i))
		values[i] = []byte(fmt.Sprintf("value:%03d", i))
	}

	batched, err := store.SetPlainBatchFresh(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	if !batched {
		t.Fatal("fresh batch fell back")
	}

	for i := 0; i < n; i++ {
		got, found, wrongType := store.GetString(string(keys[i]))
		if wrongType || !found || string(got) != string(values[i]) {
			t.Fatalf("key %q got=%q found=%t wrongType=%t", keys[i], got, found, wrongType)
		}
	}
}

func TestSetPlainBatchFreshDuplicateFallsBackWithoutMutation(t *testing.T) {
	store := New()
	keys := [][]byte{[]byte("a"), []byte("b"), []byte("a")}
	values := [][]byte{[]byte("1"), []byte("2"), []byte("3")}

	batched, err := store.SetPlainBatchFresh(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	if batched {
		t.Fatal("duplicate-key batch unexpectedly used fresh fast path")
	}
	for _, key := range []string{"a", "b"} {
		if _, found, _ := store.GetString(key); found {
			t.Fatalf("key %q mutated during duplicate fallback", key)
		}
	}
}

func TestSetPlainBatchFreshExistingKeyFallsBackWithoutPartialMutation(t *testing.T) {
	store := New()
	if err := store.SetPlain("existing", []byte("old")); err != nil {
		t.Fatal(err)
	}

	keys := [][]byte{[]byte("fresh-a"), []byte("existing"), []byte("fresh-b")}
	values := [][]byte{[]byte("a"), []byte("new"), []byte("b")}

	batched, err := store.SetPlainBatchFresh(keys, values)
	if err != nil {
		t.Fatal(err)
	}
	if batched {
		t.Fatal("batch with existing key unexpectedly used fresh fast path")
	}

	for _, key := range []string{"fresh-a", "fresh-b"} {
		if _, found, _ := store.GetString(key); found {
			t.Fatalf("fresh key %q was partially published", key)
		}
	}
	got, found, wrongType := store.GetString("existing")
	if wrongType || !found || string(got) != "old" {
		t.Fatalf("existing value got=%q found=%t wrongType=%t", got, found, wrongType)
	}
}
