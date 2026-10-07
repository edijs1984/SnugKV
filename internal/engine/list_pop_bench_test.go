package engine

import (
	"bytes"
	"fmt"
	"testing"
)

// Queue-style access: refill with RPUSH, drain with LPOP (and RPOP), and read a
// short LRANGE window. These show how cost scales with list length.
func benchQueueList(b *testing.B, card int, op func(s *Store, key string)) {
	value := bytes.Repeat([]byte("v"), 64)
	s := New()
	for i := 0; i < card; i++ {
		if _, err := s.ListPushRight("q", [][]byte{value}); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		op(s, "q")
	}
}

func BenchmarkListQueue(b *testing.B) {
	value := bytes.Repeat([]byte("v"), 64)
	for _, card := range []int{10, 100, 1000, 10000} {
		b.Run(fmt.Sprintf("lpop+rpush/card%d", card), func(b *testing.B) {
			benchQueueList(b, card, func(s *Store, key string) {
				if _, err := s.ListPopLeft(key, 1); err != nil {
					b.Fatal(err)
				}
				if _, err := s.ListPushRight(key, [][]byte{value}); err != nil {
					b.Fatal(err)
				}
			})
		})
		b.Run(fmt.Sprintf("rpop+rpush/card%d", card), func(b *testing.B) {
			benchQueueList(b, card, func(s *Store, key string) {
				if _, err := s.ListPopRight(key, 1); err != nil {
					b.Fatal(err)
				}
				if _, err := s.ListPushRight(key, [][]byte{value}); err != nil {
					b.Fatal(err)
				}
			})
		})
		b.Run(fmt.Sprintf("lrange0-9/card%d", card), func(b *testing.B) {
			benchQueueList(b, card, func(s *Store, key string) {
				if _, err := s.ListRange(key, 0, 9); err != nil {
					b.Fatal(err)
				}
			})
		})
	}
}
