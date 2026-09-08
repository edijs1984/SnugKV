// snugsoak runs a mixed-temperature correctness and memory-growth workload.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"snugkv/internal/engine"
	"snugkv/internal/optimizer"
	"sync"
	"sync/atomic"
	"time"
)

type result struct {
	DurationSeconds float64            `json:"duration_seconds"`
	Keys            int                `json:"keys"`
	Workers         int                `json:"workers"`
	Operations      uint64             `json:"operations"`
	Reads           uint64             `json:"reads"`
	Writes          uint64             `json:"writes"`
	TTLChurn        uint64             `json:"ttl_churn"`
	CounterValue    int64              `json:"counter_value"`
	Mismatches      uint64             `json:"mismatches"`
	StartAccounted  uint64             `json:"start_accounted_bytes"`
	PeakAccounted   uint64             `json:"peak_accounted_bytes"`
	EndAccounted    uint64             `json:"end_accounted_bytes"`
	HeapAlloc       uint64             `json:"process_heap_alloc"`
	Optimizer       optimizer.Stats    `json:"optimizer"`
	Memory          engine.MemoryStats `json:"memory"`
	Inspection      engine.Inspection  `json:"inspection"`
}

func valueFor(key, size int) []byte {
	prefix := []byte(fmt.Sprintf(`{"kind":"session","key":%d,"active":true,"region":"eu-north","payload":"`, key))
	suffix := []byte(`"}`)
	if size < len(prefix)+len(suffix) {
		size = len(prefix) + len(suffix)
	}
	v := make([]byte, size)
	n := copy(v, prefix)
	for n < size-len(suffix) {
		v[n] = byte('a' + key%26)
		n++
	}
	copy(v[n:], suffix)
	return v
}

func main() {
	duration := flag.Duration("duration", 24*time.Hour, "workload duration")
	keys := flag.Int("keys", 100000, "stable data keys")
	workers := flag.Int("workers", runtime.NumCPU(), "concurrent workers")
	size := flag.Int("bytes", 512, "value bytes")
	seed := flag.Int64("seed", 1, "random seed")
	flag.Parse()
	if *duration <= 0 || *keys < 10 || *workers < 1 || *size < 1 {
		fmt.Fprintln(os.Stderr, "duration and sizes must be positive; keys must be at least 10")
		os.Exit(2)
	}
	store, err := engine.NewWithOptions(engine.Options{Shards: 256, Encoding: true, ShapeEncoding: true, Compression: true})
	if err != nil {
		panic(err)
	}
	for key := 0; key < *keys; key++ {
		if err = store.Set(fmt.Sprintf("data:%d", key), valueFor(key, *size), 0); err != nil {
			panic(err)
		}
	}
	if err = store.Set("counter", []byte("0"), 0); err != nil {
		panic(err)
	}
	opt, err := optimizer.New(store, optimizer.Default())
	if err != nil {
		panic(err)
	}
	defer opt.Close()
	startMemory := store.Memory().AccountedBytes
	peakMemory := startMemory
	deadline := time.Now().Add(*duration)
	var operations, reads, writes, ttlChurn, mismatches uint64
	var wg sync.WaitGroup
	for worker := 0; worker < *workers; worker++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(*seed + int64(id+1)))
			for time.Now().Before(deadline) {
				key := rng.Intn(*keys / 10)
				if rng.Intn(10) == 0 {
					key = *keys/10 + rng.Intn(*keys-*keys/10)
				}
				got, ok := store.Get(fmt.Sprintf("data:%d", key))
				if !ok || !bytes.Equal(got, valueFor(key, *size)) {
					atomic.AddUint64(&mismatches, 1)
				}
				atomic.AddUint64(&reads, 1)
				n := atomic.AddUint64(&operations, 1)
				if n%100 == 0 {
					if err := store.Set(fmt.Sprintf("data:%d", key), valueFor(key, *size), 0); err != nil {
						panic(err)
					}
					if _, err := store.Incr("counter"); err != nil {
						panic(err)
					}
					atomic.AddUint64(&writes, 1)
				}
				if n%1000 == 0 {
					ttlKey := fmt.Sprintf("ttl:%d", id)
					if err := store.SetWithTTL(ttlKey, []byte("temporary"), 250*time.Millisecond); err != nil {
						panic(err)
					}
					store.Persist(ttlKey)
					store.Expire(ttlKey, 250*time.Millisecond)
					atomic.AddUint64(&ttlChurn, 1)
				}
			}
		}(worker)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	for {
		select {
		case <-ticker.C:
			store.CleanupExpiredLimit(1024)
			opt.Sample(256)
			if used := store.Memory().AccountedBytes; used > peakMemory {
				peakMemory = used
			}
		case <-done:
			ticker.Stop()
			goto finished
		}
	}

finished:
	store.CleanupExpiredLimit(1 << 20)
	if used := store.Memory().AccountedBytes; used > peakMemory {
		peakMemory = used
	}
	counterBytes, ok := store.Get("counter")
	var counter int64
	if !ok {
		atomic.AddUint64(&mismatches, 1)
	} else if _, err = fmt.Sscan(string(counterBytes), &counter); err != nil || counter != int64(atomic.LoadUint64(&writes)) {
		atomic.AddUint64(&mismatches, 1)
	}
	runtime.GC()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	out := result{DurationSeconds: duration.Seconds(), Keys: *keys, Workers: *workers,
		Operations: atomic.LoadUint64(&operations), Reads: atomic.LoadUint64(&reads), Writes: atomic.LoadUint64(&writes),
		TTLChurn: atomic.LoadUint64(&ttlChurn), CounterValue: counter, Mismatches: atomic.LoadUint64(&mismatches),
		StartAccounted: startMemory, PeakAccounted: peakMemory, EndAccounted: store.Memory().AccountedBytes,
		HeapAlloc: memory.HeapAlloc, Optimizer: opt.Stats(), Memory: store.Memory(), Inspection: store.Inspect()}
	if err = json.NewEncoder(os.Stdout).Encode(out); err != nil {
		panic(err)
	}
	if out.Mismatches != 0 {
		os.Exit(1)
	}
}
