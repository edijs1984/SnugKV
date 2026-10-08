package engine

import (
	"fmt"
	"testing"
)

func benchSetStore(b *testing.B, n int) (*Store, [][]byte) {
	b.Helper()
	s := New()
	members := make([][]byte, n)
	for i := range members {
		members[i] = []byte(fmt.Sprintf("member-%016d", i*7919))
	}
	for _, m := range members {
		if _, err := s.SetAdd("k", [][]byte{m}); err != nil {
			b.Fatal(err)
		}
	}
	return s, members
}

func BenchmarkSetMediumOps(b *testing.B) {
	for _, n := range []int{20, 100} {
		s, members := benchSetStore(b, n)
		b.Run(fmt.Sprintf("contains/%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if ok, _ := s.SetContains("k", members[i%n]); !ok {
					b.Fatal("missing")
				}
			}
		})
		b.Run(fmt.Sprintf("random/%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := s.SetRandomMembers("k", 1); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("members/%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := s.SetMembers("k"); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("add-existing/%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := s.SetAdd("k", [][]byte{members[i%n]}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
