package engine

import "testing"

func TestTopKOracleCoreAndEjection(t *testing.T) {
	store := New()

	if err := store.TopKReserve("custom", 3, true, 50, 5, 0.9); err != nil {
		t.Fatal(err)
	}
	results, err := store.TopKAdd(
		"custom",
		[][]byte{[]byte("a"), []byte("b"), []byte("c")},
		[]uint32{1, 1, 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	for i, result := range results {
		if result.HasExpelled {
			t.Fatalf("ADD[%d] unexpectedly expelled %q", i, result.Expelled)
		}
	}
	query, err := store.TopKQuery("custom", [][]byte{[]byte("a"), []byte("b"), []byte("c"), []byte("z")})
	if err != nil {
		t.Fatal(err)
	}
	wantQuery := []bool{true, true, true, false}
	for i := range wantQuery {
		if query[i] != wantQuery[i] {
			t.Fatalf("QUERY[%d]=%v want=%v", i, query[i], wantQuery[i])
		}
	}
	counts, err := store.TopKCount("custom", [][]byte{[]byte("a"), []byte("b"), []byte("c"), []byte("z")})
	if err != nil {
		t.Fatal(err)
	}
	wantCounts := []uint32{1, 1, 1, 0}
	for i := range wantCounts {
		if counts[i] != wantCounts[i] {
			t.Fatalf("COUNT[%d]=%d want=%d", i, counts[i], wantCounts[i])
		}
	}
	list, err := store.TopKList("custom")
	if err != nil {
		t.Fatal(err)
	}
	wantItems := []string{"a", "c", "b"}
	for i := range wantItems {
		if i >= len(list) || string(list[i].Item) != wantItems[i] || list[i].Count != 1 {
			t.Fatalf("LIST=%+v", list)
		}
	}

	if err := store.TopKReserve("rank", 2, true, 100, 5, 0.9); err != nil {
		t.Fatal(err)
	}
	results, err = store.TopKAdd(
		"rank",
		[][]byte{[]byte("a"), []byte("b")},
		[]uint32{10, 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].HasExpelled || results[1].HasExpelled {
		t.Fatalf("initial rank expelled=%+v", results)
	}
	results, err = store.TopKAdd("rank", [][]byte{[]byte("c")}, []uint32{30})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].HasExpelled || string(results[0].Expelled) != "a" {
		t.Fatalf("c insert result=%+v", results)
	}
	query, _ = store.TopKQuery("rank", [][]byte{[]byte("a"), []byte("b"), []byte("c")})
	wantQuery = []bool{false, true, true}
	for i := range wantQuery {
		if query[i] != wantQuery[i] {
			t.Fatalf("rank QUERY=%v", query)
		}
	}
	counts, _ = store.TopKCount("rank", [][]byte{[]byte("a"), []byte("b"), []byte("c")})
	wantCounts = []uint32{10, 20, 30}
	for i := range wantCounts {
		if counts[i] != wantCounts[i] {
			t.Fatalf("rank COUNT=%v", counts)
		}
	}
}

func TestTopKZeroIncrementOracle(t *testing.T) {
	store := New()
	if err := store.TopKReserve("zero", 2, true, 100, 5, 0.9); err != nil {
		t.Fatal(err)
	}
	results, err := store.TopKAdd("zero", [][]byte{[]byte("x")}, []uint32{0})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].HasExpelled {
		t.Fatalf("zero result=%+v", results)
	}
	query, err := store.TopKQuery("zero", [][]byte{[]byte("x")})
	if err != nil || len(query) != 1 || !query[0] {
		t.Fatalf("zero query=%v err=%v", query, err)
	}
	counts, err := store.TopKCount("zero", [][]byte{[]byte("x")})
	if err != nil || len(counts) != 1 || counts[0] != 0 {
		t.Fatalf("zero count=%v err=%v", counts, err)
	}
	list, err := store.TopKList("zero")
	if err != nil || len(list) != 0 {
		t.Fatalf("zero list=%+v err=%v", list, err)
	}
}

func TestTopKTTLAndPersistence(t *testing.T) {
	store := New()
	if err := store.TopKReserve("tk", 2, true, 100, 5, 0.9); err != nil {
		t.Fatal(err)
	}
	if !store.Expire("tk", 60_000_000) {
		t.Fatal("expire failed")
	}
	before := store.TTL("tk", true)
	if _, err := store.TopKAdd("tk", [][]byte{[]byte("a"), []byte("b")}, []uint32{10, 20}); err != nil {
		t.Fatal(err)
	}
	after := store.TTL("tk", true)
	if after <= 0 || after > before {
		t.Fatalf("TTL before=%d after=%d", before, after)
	}
	records := store.Export(nil)
	if len(records) != 1 || ValueType(records[0].ValueType) != TypeTopK {
		t.Fatalf("records=%+v", records)
	}
	target := New()
	if err := target.Restore(records, false); err != nil {
		t.Fatal(err)
	}
	counts, err := target.TopKCount("tk", [][]byte{[]byte("a"), []byte("b")})
	if err != nil || len(counts) != 2 || counts[0] != 10 || counts[1] != 20 {
		t.Fatalf("restored counts=%v err=%v", counts, err)
	}
}
