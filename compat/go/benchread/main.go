package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

type result struct {
	Target        string  `json:"target"`
	Addr          string  `json:"addr"`
	Keys          int     `json:"keys"`
	Workers       int     `json:"workers"`
	ReadSeconds   float64 `json:"read_seconds"`
	ReadOpsPerSec float64 `json:"read_ops_per_second"`
	GetP50US      float64 `json:"get_p50_us"`
	GetP95US      float64 `json:"get_p95_us"`
	GetP99US      float64 `json:"get_p99_us"`
	Errors        uint64  `json:"errors"`
	Mismatches    uint64  `json:"mismatches"`
}

func main() {
	target := flag.String("target", "snugkv", "label for output")
	addr := flag.String("addr", "127.0.0.1:6380", "RESP2 server address")
	keys := flag.Int("keys", 100000, "number of existing session keys")
	workers := flag.Int("workers", 4, "parallel readers")
	pool := flag.Int("pool", 16, "client connection pool size")
	flag.Parse()

	ctx := context.Background()

	client := redis.NewClient(&redis.Options{
		Addr:         *addr,
		Protocol:     2,
		PoolSize:     *pool,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	})
	defer client.Close()

	if err := client.Ping(ctx).Err(); err != nil {
		panic(err)
	}

	samples := make([]int64, *keys)
	var errors uint64
	var mismatches uint64

	start := time.Now()

	runParallel(*keys, *workers, func(i int) {
		key := fmt.Sprintf("bench:session:%08d", i)
		expected := sessionValue(i)

		before := time.Now()
		got, err := client.Get(ctx, key).Result()
		samples[i] = time.Since(before).Nanoseconds()

		if err != nil {
			atomic.AddUint64(&errors, 1)
			return
		}

		if got != expected {
			atomic.AddUint64(&mismatches, 1)
		}
	})

	duration := time.Since(start)

	sort.Slice(samples, func(i, j int) bool {
		return samples[i] < samples[j]
	})

	out := result{
		Target:        *target,
		Addr:          *addr,
		Keys:          *keys,
		Workers:       *workers,
		ReadSeconds:   duration.Seconds(),
		ReadOpsPerSec: float64(*keys) / duration.Seconds(),
		GetP50US:      float64(percentile(samples, 50)) / 1000,
		GetP95US:      float64(percentile(samples, 95)) / 1000,
		GetP99US:      float64(percentile(samples, 99)) / 1000,
		Errors:        errors,
		Mismatches:    mismatches,
	}

	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		panic(err)
	}

	if errors != 0 || mismatches != 0 {
		os.Exit(1)
	}
}

func runParallel(n, workers int, fn func(int)) {
	var next int64
	var wg sync.WaitGroup

	wg.Add(workers)

	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()

			for {
				i := int(atomic.AddInt64(&next, 1) - 1)
				if i >= n {
					return
				}

				fn(i)
			}
		}()
	}

	wg.Wait()
}

func percentile(samples []int64, p int) int64 {
	idx := (len(samples) * p) / 100
	if idx >= len(samples) {
		idx = len(samples) - 1
	}
	return samples[idx]
}

func sessionValue(i int) string {
	return fmt.Sprintf(
		`{"id":"%08x-0000-4000-8000-000000000000","country":"LV","plan":"free","status":"active","active":true,"user":%d,"createdAt":"2026-09-07T12:34:56Z","roles":["reader","member"],"permissions":["profile.read","session.refresh","notifications.read"],"preferences":{"language":"lv","timezone":"Europe/Riga","theme":"dark","digest":true},"organization":{"id":"org-00000001","name":"Example Organization","tier":"standard"},"featureFlags":{"newDashboard":true,"auditLog":false,"betaSearch":false},"lastSeen":{"ip":"192.0.2.1","device":"web","region":"eu-north"}}`,
		i,
		i,
	)
}
