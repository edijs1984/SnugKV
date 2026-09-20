package engine

import (
	"bytes"
	"testing"
)

func TestZSetAlgebraStoreOOMLeavesDestinationUntouched(t *testing.T) {
	s, err := NewWithOptions(Options{Shards: 1})
	if err != nil {
		t.Fatal(err)
	}

	large := make([]ZSetItem, 0, 32)
	for i := 0; i < 32; i++ {
		member := append([]byte{byte('a' + i%26), byte('A' + i%26)}, bytes.Repeat([]byte("x"), 1022)...)
		large = append(large, ZSetItem{Member: member, Score: float64(i)})
	}
	if _, _, _, err := s.ZSetAdd("source", large, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.ZSetAdd("dest", []ZSetItem{zitem(99, "old")}, ZSetAddOptions{}); err != nil {
		t.Fatal(err)
	}
	before, err := s.ZSetRange("dest", 0, -1, false)
	if err != nil {
		t.Fatal(err)
	}
	beforeMemory := s.Memory()

	s.memory.mu.Lock()
	max := s.memory.used
	s.memory.mu.Unlock()
	s.memory.max.Store(max)

	if _, err := s.ZSetUnionStore("dest", []string{"source"}, nil, ZSetAggregateSum); err != ErrOOM {
		t.Fatalf("store err=%v want ErrOOM", err)
	}
	after, err := s.ZSetRange("dest", 0, -1, false)
	if err != nil {
		t.Fatal(err)
	}
	assertZSetItems(t, after, before)
	if memory := s.Memory(); memory.AccountedBytes != beforeMemory.AccountedBytes {
		t.Fatalf("failed STORE changed accounted memory: before=%d after=%d", beforeMemory.AccountedBytes, memory.AccountedBytes)
	}
}
