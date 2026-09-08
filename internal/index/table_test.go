package index

import (
	"fmt"
	"testing"
)

func TestCollisionChurn(t *testing.T) {
	table := New[int]()
	table.hash = func(string) uint64 { return 7 }
	for i := 0; i < 1000; i++ {
		table.Set(fmt.Sprint(i), i)
	}
	for i := 0; i < 1000; i += 2 {
		table.Delete(fmt.Sprint(i))
	}
	for i := 1; i < 1000; i += 2 {
		v, ok := table.Get(fmt.Sprint(i))
		if !ok || v != i {
			t.Fatal("collision lost key")
		}
	}
	before := table.CapacityBytes()
	table.Compact()
	if table.CapacityBytes() > before {
		t.Fatal("compaction grew index")
	}
	for i := 0; i < 1000; i += 2 {
		table.Set(fmt.Sprint(i), i)
	}
	if table.Len() != 1000 {
		t.Fatal(table.Len())
	}
	for key, value := range table.All() {
		if key != fmt.Sprint(value) {
			t.Fatal("iterator mismatch")
		}
	}
}
func TestGrowthAccounting(t *testing.T) {
	table := New[int]()
	for i := 0; i < 100; i++ {
		before := table.CapacityBytes()
		growth := table.GrowthBytes(1)
		table.Set(fmt.Sprint(i), i)
		if before+growth != table.CapacityBytes() {
			t.Fatal("growth mismatch")
		}
	}
}
