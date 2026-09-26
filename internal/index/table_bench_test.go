package index

import (
	"fmt"
	"testing"
)

func BenchmarkGetHashedBytesShardSized(b *testing.B) {
	const keysN = 4096

	table := New[uint32]()
	keys := make([][]byte, keysN)
	hashes := make([]uint64, keysN)

	for i := 0; i < keysN; i++ {
		key := fmt.Sprintf("key:%012d", i)
		table.Set(key, uint32(i))
		keys[i] = []byte(key)
		hashes[i] = HashBytes(keys[i])
	}

	b.ReportAllocs()
	b.ResetTimer()

	var sink uint32
	for i := 0; i < b.N; i++ {
		n := i & (keysN - 1)
		got, ok := table.GetHashedBytes(keys[n], hashes[n])
		if !ok {
			b.Fatal("lookup missed")
		}
		sink += got
	}

	if sink == ^uint32(0) {
		b.Fatal("unreachable")
	}
}
