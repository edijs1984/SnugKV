package engine

import (
	"fmt"
	"testing"
)

func BenchmarkSetPlainBatchFresh256(b *testing.B) {
	const batchSize = 256

	keys := make([][]byte, batchSize)
	values := make([][]byte, batchSize)
	for i := 0; i < batchSize; i++ {
		keys[i] = []byte(fmt.Sprintf("batch:%03d", i))
		values[i] = make([]byte, 256)
		for j := range values[i] {
			values[i][j] = byte(i*31 + j)
		}
	}

	b.ReportAllocs()
	b.SetBytes(batchSize * 256)

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		store := New()
		b.StartTimer()

		batched, err := store.SetPlainBatchFresh(keys, values)
		if err != nil {
			b.Fatal(err)
		}
		if !batched {
			b.Fatal("fresh batch fell back")
		}
	}
}
