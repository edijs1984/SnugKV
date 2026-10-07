package engine

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// BenchmarkSetBatchContention drives concurrent pipelined SET batches of
// fresh keys, like the lab's load workload, and reports per-batch p50/p99.
func BenchmarkSetBatchContention(b *testing.B) {
	const batchSize = 256
	modes := []struct {
		name string
		fn   func(*Store, [][]byte, [][]byte) (bool, error)
	}{
		{"global", (*Store).SetPlainBatchFresh},
		{"shardlocal", (*Store).SetPlainBatchFreshShardLocal},
	}
	for _, mode := range modes {
		b.Run(mode.name, func(b *testing.B) {
			store := New()
			var next atomic.Uint64
			var mu sync.Mutex
			var lat []time.Duration
			b.SetParallelism(4)
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				keys := make([][]byte, batchSize)
				values := make([][]byte, batchSize)
				for i := range values {
					values[i] = []byte("1234567890")
				}
				local := make([]time.Duration, 0, 4096)
				for pb.Next() {
					base := next.Add(batchSize) - batchSize
					for i := range keys {
						keys[i] = []byte(fmt.Sprintf("counter:%d", base+uint64(i)))
					}
					start := time.Now()
					ok, err := mode.fn(store, keys, values)
					d := time.Since(start)
					if err != nil || !ok {
						b.Errorf("batch failed: ok=%v err=%v", ok, err)
						return
					}
					local = append(local, d)
				}
				mu.Lock()
				lat = append(lat, local...)
				mu.Unlock()
			})
			b.StopTimer()
			if len(lat) == 0 {
				return
			}
			sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
			b.ReportMetric(float64(lat[len(lat)/2].Microseconds()), "p50-µs/batch")
			b.ReportMetric(float64(lat[len(lat)*99/100].Microseconds()), "p99-µs/batch")
		})
	}
}
