package engine

import "testing"

func TestBloomScalableExpansionInfo(t *testing.T) {
	store := New()
	if err := store.BloomReserveWithOptions("bf", 0.01, 2, BloomOptions{Expansion: 2}); err != nil {
		t.Fatal(err)
	}

	want := []struct {
		capacity uint64
		size     uint64
		filters  uint64
		items    uint64
	}{
		{2, 104, 1, 1},
		{2, 104, 1, 2},
		{6, 176, 2, 3},
		{6, 176, 2, 4},
		{6, 176, 2, 5},
		{6, 176, 2, 6},
		{14, 256, 3, 7},
		{14, 256, 3, 8},
	}

	for i, expected := range want {
		added, err := store.BloomAdd("bf", []byte{byte('a' + i)})
		if err != nil || !added {
			t.Fatalf("add %d added=%v err=%v", i+1, added, err)
		}
		info, err := store.BloomInfo("bf")
		if err != nil {
			t.Fatal(err)
		}
		if info.Capacity != expected.capacity || info.Size != expected.size ||
			info.Filters != expected.filters || info.Items != expected.items ||
			info.Expansion != 2 || !info.Scaling {
			t.Fatalf("after %d: info=%+v want capacity=%d size=%d filters=%d items=%d",
				i+1, info, expected.capacity, expected.size, expected.filters, expected.items)
		}
	}
}

func TestBloomExpansionThreeAndNonScaling(t *testing.T) {
	store := New()
	if err := store.BloomReserveWithOptions("scale3", 0.01, 2, BloomOptions{Expansion: 3}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 9; i++ {
		if _, err := store.BloomAdd("scale3", []byte{byte(i), 1}); err != nil {
			t.Fatal(err)
		}
	}
	info, err := store.BloomInfo("scale3")
	if err != nil {
		t.Fatal(err)
	}
	if info.Capacity != 26 || info.Size != 280 || info.Filters != 3 ||
		info.Items != 9 || info.Expansion != 3 || !info.Scaling {
		t.Fatalf("scale3 info=%+v", info)
	}

	if err := store.BloomReserveWithOptions("fixed", 0.01, 2, BloomOptions{NonScaling: true}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []string{"a", "b"} {
		if _, err := store.BloomAdd("fixed", []byte(item)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.BloomAdd("fixed", []byte("c")); err == nil || err.Error() != "ERR non scaling filter is full" {
		t.Fatalf("full error=%v", err)
	}
	info, err = store.BloomInfo("fixed")
	if err != nil {
		t.Fatal(err)
	}
	if info.Capacity != 2 || info.Size != 104 || info.Filters != 1 ||
		info.Items != 2 || info.Scaling {
		t.Fatalf("fixed info=%+v", info)
	}
}

func TestBloomInsertNonScalingReturnsPerItemError(t *testing.T) {
	store := New()
	results, err := store.BloomInsertWithOptions(
		"bf", 2, 0.01, BloomOptions{NonScaling: true},
		[][]byte{[]byte("a"), []byte("b"), []byte("c")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || !results[0].Added || !results[1].Added ||
		results[2].Err == nil || results[2].Err.Error() != "ERR non scaling filter is full" {
		t.Fatalf("results=%+v", results)
	}
	info, err := store.BloomInfo("bf")
	if err != nil {
		t.Fatal(err)
	}
	if info.Items != 2 {
		t.Fatalf("items=%d want=2", info.Items)
	}
}
