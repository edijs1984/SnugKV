package engine

import (
	"bytes"
	"testing"
)

func TestCuckooOracleCollisionCounts(t *testing.T) {
	store := New()
	results, err := store.CuckooInsert("ins", 5, [][]byte{
		[]byte("a"), []byte("a"), []byte("b"), []byte("c"),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	for i, added := range results {
		if !added {
			t.Fatalf("insert result %d = false", i)
		}
	}
	count, err := store.CuckooCount("ins", []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("COUNT a = %d, want 4", count)
	}

	results, err = store.CuckooInsert("insnx", 5, [][]byte{
		[]byte("a"), []byte("a"), []byte("b"), []byte("c"),
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []bool{true, false, true, true}
	for i := range want {
		if results[i] != want[i] {
			t.Fatalf("INSERTNX[%d] = %v, want %v", i, results[i], want[i])
		}
	}
	count, err = store.CuckooCount("insnx", []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("INSERTNX COUNT a = %d, want 2", count)
	}
}

func TestCuckooTTLAndPersistence(t *testing.T) {
	store := New()
	if err := store.CuckooReserve("cf", 10); err != nil {
		t.Fatal(err)
	}
	if !store.Expire("cf", 60_000_000) {
		t.Fatal("expire failed")
	}
	before := store.TTL("cf", true)
	if before <= 0 {
		t.Fatalf("TTL before add=%d", before)
	}
	added, err := store.CuckooAdd("cf", []byte("alice"), false)
	if err != nil || !added {
		t.Fatalf("add=%v err=%v", added, err)
	}
	after := store.TTL("cf", true)
	if after <= 0 || after > before {
		t.Fatalf("TTL not preserved before=%d after=%d", before, after)
	}

	records := store.Export(nil)
	if len(records) != 1 || ValueType(records[0].ValueType) != TypeCuckoo {
		t.Fatalf("export=%+v", records)
	}
	target := New()
	if err := target.Restore(records, false); err != nil {
		t.Fatal(err)
	}
	found, err := target.CuckooExists("cf", []byte("alice"))
	if err != nil || !found {
		t.Fatalf("restored exists=%v err=%v", found, err)
	}
	if !bytes.Equal(records[0].Value, target.Export(nil)[0].Value) {
		t.Fatal("Cuckoo bytes changed across restore")
	}
}
