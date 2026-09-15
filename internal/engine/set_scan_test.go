package engine

import (
	"bytes"
	"testing"
)

func TestSetMultiContainsAndScan(t *testing.T) {
	s := New()
	if _, err := s.SetAdd("letters", [][]byte{[]byte("bb"), []byte("aa"), []byte("ba"), []byte("ab")}); err != nil {
		t.Fatal(err)
	}

	found, err := s.SetMultiContains("letters", [][]byte{[]byte("aa"), []byte("missing"), []byte("bb")})
	if err != nil {
		t.Fatal(err)
	}
	wantFound := []bool{true, false, true}
	if len(found) != len(wantFound) {
		t.Fatalf("SMISMEMBER len=%d want %d", len(found), len(wantFound))
	}
	for i := range wantFound {
		if found[i] != wantFound[i] {
			t.Fatalf("SMISMEMBER[%d]=%v want %v", i, found[i], wantFound[i])
		}
	}

	next, members, err := s.SetScan("letters", 0, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if next != 2 {
		t.Fatalf("first cursor=%d want 2", next)
	}
	want := [][]byte{[]byte("aa"), []byte("ab")}
	if len(members) != len(want) {
		t.Fatalf("first scan len=%d want %d", len(members), len(want))
	}
	for i := range want {
		if !bytes.Equal(members[i], want[i]) {
			t.Fatalf("first scan[%d]=%q want %q", i, members[i], want[i])
		}
	}

	next, members, err = s.SetScan("letters", 2, 2, []byte("b*"))
	if err != nil {
		t.Fatal(err)
	}
	if next != 0 {
		t.Fatalf("second cursor=%d want 0", next)
	}
	want = [][]byte{[]byte("ba"), []byte("bb")}
	if len(members) != len(want) {
		t.Fatalf("second scan len=%d want %d", len(members), len(want))
	}
	for i := range want {
		if !bytes.Equal(members[i], want[i]) {
			t.Fatalf("second scan[%d]=%q want %q", i, members[i], want[i])
		}
	}
}

func TestSetScanMissingAndWrongType(t *testing.T) {
	s := New()
	found, err := s.SetMultiContains("missing", [][]byte{[]byte("a"), []byte("b")})
	if err != nil || len(found) != 2 || found[0] || found[1] {
		t.Fatalf("missing multi contains=%v err=%v", found, err)
	}
	next, members, err := s.SetScan("missing", 0, 10, nil)
	if err != nil || next != 0 || len(members) != 0 {
		t.Fatalf("missing scan next=%d members=%v err=%v", next, members, err)
	}

	if err := s.Set("plain", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetMultiContains("plain", [][]byte{[]byte("a")}); err == nil {
		t.Fatal("SetMultiContains on string did not return WRONGTYPE")
	}
	if _, _, err := s.SetScan("plain", 0, 10, nil); err == nil {
		t.Fatal("SetScan on string did not return WRONGTYPE")
	}
}
