package engine

import "testing"

func TestCMSOracleCoreAndProbabilityDimensions(t *testing.T) {
	store := New()
	if err := store.CMSInitByDim("cms", 20, 5); err != nil {
		t.Fatal(err)
	}
	results, err := store.CMSIncrBy(
		"cms",
		[][]byte{[]byte("apple"), []byte("banana"), []byte("apple")},
		[]uint64{3, 2, 4},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []uint64{3, 2, 7}
	for i := range want {
		if results[i] != want[i] {
			t.Fatalf("INCRBY[%d]=%d want=%d", i, results[i], want[i])
		}
	}
	query, err := store.CMSQuery("cms", [][]byte{[]byte("apple"), []byte("banana"), []byte("missing")})
	if err != nil {
		t.Fatal(err)
	}
	want = []uint64{7, 2, 0}
	for i := range want {
		if query[i] != want[i] {
			t.Fatalf("QUERY[%d]=%d want=%d", i, query[i], want[i])
		}
	}
	info, err := store.CMSInfo("cms")
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 20 || info.Depth != 5 || info.Count != 9 {
		t.Fatalf("INFO=%+v", info)
	}

	if err := store.CMSInitByProb("prob", 0.01, 0.01); err != nil {
		t.Fatal(err)
	}
	info, err = store.CMSInfo("prob")
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 200 || info.Depth != 7 || info.Count != 0 {
		t.Fatalf("prob INFO=%+v", info)
	}
}

func TestCMSMergeOracle(t *testing.T) {
	store := New()
	for _, key := range []string{"a", "b", "merged", "weighted"} {
		if err := store.CMSInitByDim(key, 20, 5); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CMSIncrBy("a", [][]byte{[]byte("x"), []byte("y")}, []uint64{2, 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CMSIncrBy("b", [][]byte{[]byte("x"), []byte("z")}, []uint64{5, 7}); err != nil {
		t.Fatal(err)
	}
	if err := store.CMSMerge("merged", []string{"a", "b"}, []int64{1, 1}); err != nil {
		t.Fatal(err)
	}
	got, err := store.CMSQuery("merged", [][]byte{[]byte("x"), []byte("y"), []byte("z")})
	if err != nil {
		t.Fatal(err)
	}
	want := []uint64{7, 3, 7}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("merged[%d]=%d want=%d", i, got[i], want[i])
		}
	}
	info, _ := store.CMSInfo("merged")
	if info.Count != 17 {
		t.Fatalf("merged count=%d", info.Count)
	}

	if err := store.CMSMerge("weighted", []string{"a", "b"}, []int64{2, 3}); err != nil {
		t.Fatal(err)
	}
	got, err = store.CMSQuery("weighted", [][]byte{[]byte("x"), []byte("y"), []byte("z")})
	if err != nil {
		t.Fatal(err)
	}
	want = []uint64{19, 6, 21}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("weighted[%d]=%d want=%d", i, got[i], want[i])
		}
	}
	info, _ = store.CMSInfo("weighted")
	if info.Count != 46 {
		t.Fatalf("weighted count=%d", info.Count)
	}
}

func TestCMSTTLAndPersistence(t *testing.T) {
	store := New()
	if err := store.CMSInitByDim("cms", 20, 5); err != nil {
		t.Fatal(err)
	}
	if !store.Expire("cms", 60_000_000) {
		t.Fatal("expire failed")
	}
	before := store.TTL("cms", true)
	if _, err := store.CMSIncrBy("cms", [][]byte{[]byte("x")}, []uint64{5}); err != nil {
		t.Fatal(err)
	}
	after := store.TTL("cms", true)
	if after <= 0 || after > before {
		t.Fatalf("TTL before=%d after=%d", before, after)
	}
	records := store.Export(nil)
	if len(records) != 1 || ValueType(records[0].ValueType) != TypeCMS {
		t.Fatalf("records=%+v", records)
	}
	target := New()
	if err := target.Restore(records, false); err != nil {
		t.Fatal(err)
	}
	got, err := target.CMSQuery("cms", [][]byte{[]byte("x")})
	if err != nil || len(got) != 1 || got[0] != 5 {
		t.Fatalf("restored query=%v err=%v", got, err)
	}
}
