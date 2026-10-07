package engine

import (
	"fmt"
	"strconv"
	"testing"
)

// Mirrors cmd/redisstructurebench for sorted sets: ZADD fills one container at a
// time (idx/cardinality picks the key, score = idx), ZSCORE reads members back.

func zsetBenchMember(member, idx int) []byte {
	x := uint64(1) ^ uint64(idx+1)*0x9e3779b97f4a7c15
	return []byte(fmt.Sprintf("m:%06d:%016x", member, x))
}

func BenchmarkZSetAddSequential(b *testing.B) {
	for _, card := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("card%d", card), func(b *testing.B) {
			keys := make([]string, b.N/card+1)
			for i := range keys {
				keys[i] = "zset:" + strconv.Itoa(i)
			}
			members := make([][]byte, b.N)
			for i := range members {
				members[i] = zsetBenchMember(i%card, i)
			}
			s := New()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				pairs := []ZSetItem{{Member: members[i], Score: float64(i)}}
				if _, _, _, err := s.ZSetAdd(keys[i/card], pairs, ZSetAddOptions{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkZSetScore(b *testing.B) {
	const items = 200_000
	for _, card := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("card%d", card), func(b *testing.B) {
			s := New()
			keys := make([]string, items/card)
			for i := range keys {
				keys[i] = "zset:" + strconv.Itoa(i)
			}
			members := make([][]byte, items)
			for i := range members {
				members[i] = zsetBenchMember(i%card, i)
			}
			for i := 0; i < items; i++ {
				pairs := []ZSetItem{{Member: members[i], Score: float64(i)}}
				if _, _, _, err := s.ZSetAdd(keys[i/card], pairs, ZSetAddOptions{}); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				idx := (i * 7919) % items
				if _, ok, err := s.ZSetScore(keys[idx/card], members[idx]); err != nil || !ok {
					b.Fatalf("ZSCORE ok=%v err=%v", ok, err)
				}
			}
		})
	}
}
