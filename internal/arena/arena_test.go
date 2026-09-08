package arena

import (
	"bytes"
	"testing"
)

func TestReuseAndGeneration(t *testing.T) {
	var a Arena
	before := a.MemoryBytes()
	growth := a.GrowthFor([]int{5})
	ref := a.Alloc([]byte("hello"))
	if a.MemoryBytes() != before+growth {
		t.Fatal("growth accounting")
	}
	got, err := a.View(ref)
	if err != nil || string(got) != "hello" {
		t.Fatal(err)
	}
	a.Free(ref)
	if _, err = a.View(ref); err == nil {
		t.Fatal("freed reference accepted")
	}
	before = a.MemoryBytes()
	next := a.Alloc([]byte("world"))
	if a.MemoryBytes() != before {
		t.Fatal("free block not reused")
	}
	if _, err = a.View(ref); err == nil {
		t.Fatal("old generation accepted")
	}
	if got, _ = a.View(next); string(got) != "world" {
		t.Fatal("reused value")
	}
}
func TestBatchProjection(t *testing.T) {
	var a Arena
	lengths := []int{0, 1, 100, 65000, 65536, 130000, 10}
	growth := a.GrowthFor(lengths)
	for _, n := range lengths {
		value := bytes.Repeat([]byte{42}, n)
		ref := a.Alloc(value)
		got, err := a.View(ref)
		if err != nil || !bytes.Equal(value, got) {
			t.Fatal("roundtrip")
		}
	}
	if a.MemoryBytes() != growth {
		t.Fatalf("got %d want %d", a.MemoryBytes(), growth)
	}
}
