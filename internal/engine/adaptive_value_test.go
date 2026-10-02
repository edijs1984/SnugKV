package engine

import (
	"bytes"
	"testing"
)

func TestAdaptiveRawStringUsesDirectVisitorWithEncodingEnabled(t *testing.T) {
	store, err := NewWithOptions(Options{
		Shards:        1,
		Encoding:      true,
		Compression:   true,
		ShapeEncoding: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	value := bytes.Repeat([]byte("incompressible-ish-payload:"), 8)
	if len(value) <= 36 {
		t.Fatal("test value must bypass synchronous scalar codecs")
	}
	if err := store.Set("raw", value, 0); err != nil {
		t.Fatal(err)
	}

	var got []byte
	handled, err := store.VisitRawStringBytes([]byte("raw"), func(view []byte) error {
		got = append(got[:0], view...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("adaptive store did not use RAW direct visitor")
	}
	if !bytes.Equal(got, value) {
		t.Fatalf("value mismatch: got %q want %q", got, value)
	}
}

func TestAdaptiveEncodedScalarFallsBackFromRawVisitor(t *testing.T) {
	store, err := NewWithOptions(Options{
		Shards:   1,
		Encoding: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Set("n", []byte("123456"), 0); err != nil {
		t.Fatal(err)
	}

	handled, err := store.VisitRawStringBytes([]byte("n"), func([]byte) error {
		t.Fatal("encoded scalar must not use RAW direct visitor")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("encoded scalar unexpectedly reported RAW handling")
	}

	got, found, wrongType := store.GetStringBytesInto([]byte("n"), nil)
	if wrongType || !found || string(got) != "123456" {
		t.Fatalf("fallback get got=%q found=%t wrongType=%t", got, found, wrongType)
	}
}

func TestAdaptiveRawStableDropsOptimizerMetadataAndEnablesDirectRead(t *testing.T) {
	store, err := NewWithOptions(Options{
		Shards:      1,
		Encoding:    true,
		Compression: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	value := make([]byte, 256)
	for i := range value {
		value[i] = byte((i*73 + 19) & 0xff)
	}

	handled, err := store.SetPlainBatchFresh(
		[][]byte{[]byte("raw-stable")},
		[][]byte{value},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("fresh SET batch was not handled")
	}

	sh := store.shardFor("raw-stable")
	sh.mu.RLock()
	e, ok := sh.get("raw-stable")
	if !ok {
		sh.mu.RUnlock()
		t.Fatal("missing raw-stable entry")
	}
	if e.entryMeta == nil {
		sh.mu.RUnlock()
		t.Fatal("optimization candidate should start with metadata")
	}
	generation := e.ref.Generation()
	sh.mu.RUnlock()

	if direct, err := store.VisitRawString("raw-stable", func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	} else if direct {
		t.Fatal("unclassified adaptive RAW value bypassed activity tracking")
	}

	before := store.Memory().MetaBytes
	if !store.MarkRawStable("raw-stable", generation) {
		t.Fatal("failed to finalize unchanged RAW generation")
	}
	after := store.Memory().MetaBytes
	if after >= before {
		t.Fatalf("metadata did not shrink: before=%d after=%d", before, after)
	}

	var got []byte
	direct, err := store.VisitRawString("raw-stable", func(view []byte) error {
		got = append(got[:0], view...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !direct {
		t.Fatal("stable RAW value did not use direct visitor")
	}
	if !bytes.Equal(got, value) {
		t.Fatal("stable RAW direct read changed bytes")
	}

	if _, eligible := store.OptimizationEligible("raw-stable", 0, 0); eligible {
		t.Fatal("stable RAW value remained eligible for optimization")
	}
}
