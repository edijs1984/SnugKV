package engine

import (
	"bytes"
	"fmt"
	"strconv"
	"testing"
)

// These benchmarks mirror cmd/redisstructurebench for lists: each container is
// filled one element at a time with RPUSH (idx/cardinality picks the key) and
// read back with LINDEX at a random member position.

var listBenchCardinalities = []int{10, 100, 1000}

func listBenchKeys(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = "list:" + strconv.Itoa(i)
	}
	return keys
}

func BenchmarkListPushRightSequential(b *testing.B) {
	for _, card := range listBenchCardinalities {
		b.Run(fmt.Sprintf("card%d", card), func(b *testing.B) {
			value := bytes.Repeat([]byte("v"), 64)
			args := [][]byte{value}
			keys := listBenchKeys(b.N/card + 1)
			s := New()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.ListPushRight(keys[i/card], args); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkListIndex(b *testing.B) {
	const items = 200_000
	for _, card := range listBenchCardinalities {
		b.Run(fmt.Sprintf("card%d", card), func(b *testing.B) {
			value := bytes.Repeat([]byte("v"), 64)
			args := [][]byte{value}
			containers := items / card
			keys := listBenchKeys(containers)
			s := New()
			for i := 0; i < items; i++ {
				if _, err := s.ListPushRight(keys[i/card], args); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Deterministic scatter over all items, like the wire benchmark.
				idx := (i * 7919) % items
				if _, ok, err := s.ListIndex(keys[idx/card], int64(idx%card)); err != nil || !ok {
					b.Fatalf("LINDEX ok=%v err=%v", ok, err)
				}
			}
		})
	}
}
