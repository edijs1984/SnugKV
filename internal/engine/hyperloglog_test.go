package engine

import (
	"fmt"
	"strings"
	"testing"
)

func TestHLLSparseCreationAddCountAndRoundTrip(t *testing.T) {
	s := New()
	changed, err := s.HLLAdd("h", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("missing PFADD key should be created")
	}
	value, ok := s.Get("h")
	if !ok {
		t.Fatal("HLL key missing")
	}
	if len(value) != 18 || string(value[:4]) != "HYLL" || value[4] != hllSparseEncoding {
		t.Fatalf("empty HLL = len %d header %q encoding %d", len(value), value[:4], value[4])
	}
	if value[16] != 0x7f || value[17] != 0xff {
		t.Fatalf("empty sparse body = %x", value[16:])
	}
	if value[15]&0x80 == 0 {
		t.Fatal("new PFADD HLL cache should be invalid")
	}

	card, err := s.HLLCount([]string{"h"})
	if err != nil || card != 0 {
		t.Fatalf("empty PFCOUNT = %d, err=%v", card, err)
	}
	value, _ = s.Get("h")
	if _, valid := hllCachedCardinality(value); !valid {
		t.Fatal("single-key PFCOUNT should populate cache")
	}

	changed, err = s.HLLAdd("h", [][]byte{[]byte("a"), []byte("b"), []byte("c")})
	if err != nil || !changed {
		t.Fatalf("PFADD changed=%t err=%v", changed, err)
	}
	changed, err = s.HLLAdd("h", [][]byte{[]byte("a"), []byte("b")})
	if err != nil || changed {
		t.Fatalf("duplicate PFADD changed=%t err=%v", changed, err)
	}
	card, err = s.HLLCount([]string{"h"})
	if err != nil || card != 3 {
		t.Fatalf("PFCOUNT = %d, err=%v", card, err)
	}

	value, _ = s.Get("h")
	if err := s.Set("copy", value, 0); err != nil {
		t.Fatal(err)
	}
	copyCard, err := s.HLLCount([]string{"copy"})
	if err != nil || copyCard != card {
		t.Fatalf("restored PFCOUNT = %d, want %d, err=%v", copyCard, card, err)
	}
}

func TestHLLMergeIncludesDestinationAndMissingSources(t *testing.T) {
	s := New()
	_, _ = s.HLLAdd("a", [][]byte{[]byte("one"), []byte("two"), []byte("three")})
	_, _ = s.HLLAdd("b", [][]byte{[]byte("three"), []byte("four"), []byte("five")})

	if err := s.HLLMerge("a", []string{"b", "missing"}); err != nil {
		t.Fatal(err)
	}
	card, err := s.HLLCount([]string{"a"})
	if err != nil || card != 5 {
		t.Fatalf("merged destination count = %d, err=%v", card, err)
	}

	union, err := s.HLLCount([]string{"a", "b", "missing"})
	if err != nil || union != 5 {
		t.Fatalf("multi-key PFCOUNT = %d, err=%v", union, err)
	}

	if err := s.HLLMerge("empty", nil); err != nil {
		t.Fatal(err)
	}
	empty, err := s.HLLCount([]string{"empty"})
	if err != nil || empty != 0 {
		t.Fatalf("empty merge count = %d, err=%v", empty, err)
	}
}

func TestHLLPromotesToDenseAndEstimateIsReasonable(t *testing.T) {
	s := New()
	elements := make([][]byte, 12000)
	for i := range elements {
		elements[i] = []byte(fmt.Sprintf("member:%d", i))
	}
	changed, err := s.HLLAdd("dense", elements)
	if err != nil || !changed {
		t.Fatalf("large PFADD changed=%t err=%v", changed, err)
	}
	value, _ := s.Get("dense")
	if len(value) != hllDenseSize || value[4] != hllDenseEncoding {
		t.Fatalf("large HLL len=%d encoding=%d, want dense %d", len(value), value[4], hllDenseSize)
	}
	card, err := s.HLLCount([]string{"dense"})
	if err != nil {
		t.Fatal(err)
	}
	if card < 11500 || card > 12500 {
		t.Fatalf("PFCOUNT estimate = %d for 12000 unique values", card)
	}
}

func TestHLLInvalidRepresentations(t *testing.T) {
	s := New()
	if err := s.Set("plain", []byte("not-an-hll"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HLLCount([]string{"plain"}); err == nil || !strings.Contains(err.Error(), "not a valid HyperLogLog") {
		t.Fatalf("plain string error = %v", err)
	}

	corrupt := make([]byte, hllHeaderSize)
	copy(corrupt[:4], "HYLL")
	corrupt[4] = hllSparseEncoding
	hllInvalidateCache(corrupt)
	if err := s.Set("corrupt", corrupt, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HLLCount([]string{"corrupt"}); err == nil || !strings.Contains(err.Error(), "INVALIDOBJ") {
		t.Fatalf("corrupt sparse error = %v", err)
	}
}
