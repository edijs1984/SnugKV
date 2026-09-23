package engine

import (
	"bytes"
	"testing"
)

func TestBloomTTLAndPersistence(t *testing.T) {
	store := New()
	if err := store.BloomReserve("bf", 0.01, 100); err != nil {
		t.Fatal(err)
	}
	if !store.Expire("bf", 60_000_000) {
		t.Fatal("expire failed")
	}
	before := store.TTL("bf", true)
	if before <= 0 {
		t.Fatalf("TTL before add=%d", before)
	}
	added, err := store.BloomAdd("bf", []byte("alice"))
	if err != nil || !added {
		t.Fatalf("BloomAdd added=%v err=%v", added, err)
	}
	after := store.TTL("bf", true)
	if after <= 0 || after > before {
		t.Fatalf("TTL not preserved before=%d after=%d", before, after)
	}

	records := store.Export(nil)
	if len(records) != 1 || ValueType(records[0].ValueType) != TypeBloom {
		t.Fatalf("export=%+v", records)
	}

	target := New()
	if err := target.Restore(records, false); err != nil {
		t.Fatal(err)
	}
	found, err := target.BloomExists("bf", []byte("alice"))
	if err != nil || !found {
		t.Fatalf("restored exists=%v err=%v", found, err)
	}
	info, err := target.BloomInfo("bf")
	if err != nil {
		t.Fatal(err)
	}
	if info.Capacity != 100 || info.Items != 1 || info.Filters != 1 || info.Expansion != 2 {
		t.Fatalf("restored info=%+v", info)
	}
	if !bytes.Equal(records[0].Value, target.Export(nil)[0].Value) {
		t.Fatal("Bloom bytes changed across restore")
	}
}
