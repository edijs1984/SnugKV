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
	ValueBytes    int64   `json:"logical_value_bytes"`
	LoadSeconds   float64 `json:"load_seconds"`
	LoadOpsPerSec float64 `json:"load_ops_per_second"`
	ReadSeconds   float64 `json:"read_seconds"`
	ReadOpsPerSec float64 `json:"read_ops_per_second"`
	GetP50US      float64 `json:"get_p50_us"`
	GetP95US      float64 `json:"get_p95_us"`
	GetP99US      float64 `json:"get_p99_us"`
	Errors        uint64  `json:"errors"`
	Mismatches    uint64  `json:"mismatches"`
}

func main() {
	target := flag.String("target", "redis", "label for output")
	addr := flag.String("addr", "127.0.0.1:6379", "RESP2 server address")
	keys := flag.Int("keys", 100000, "number of session keys")
	workers := flag.Int("workers", 4, "parallel workers")
	pool := flag.Int("pool", 16, "client connection pool size")
	flag.Parse()

	if *keys < 1 || *workers < 1 || *pool < 1 {
		fmt.Fprintln(os.Stderr, "keys, workers and pool must be positive")
		os.Exit(2)
	}

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
		fmt.Fprintf(os.Stderr, "ping %s: %v\n", *addr, err)
		os.Exit(1)
	}

	values := make([]string, *keys)
	var logicalBytes int64
	for i := 0; i < *keys; i++ {
		v := sessionValue(i)
		values[i] = v
		logicalBytes += int64(len(v))
	}

	if err := client.FlushAll(ctx).Err(); err != nil {
		fmt.Fprintf(os.Stderr, "flushall: %v\n", err)
		os.Exit(1)
	}

	var errors uint64
	loadStart := time.Now()
	runParallel(*keys, *workers, func(i int) {
		key := fmt.Sprintf("bench:session:%08d", i)
		if err := client.Set(ctx, key, values[i], 0).Err(); err != nil {
			atomic.AddUint64(&errors, 1)
		}
	})
	loadDuration := time.Since(loadStart)

	samples := make([]int64, *keys)
	var mismatches uint64
	readStart := time.Now()
	runParallel(*keys, *workers, func(i int) {
		key := fmt.Sprintf("bench:session:%08d", i)
		start := time.Now()
		got, err := client.Get(ctx, key).Result()
		samples[i] = time.Since(start).Nanoseconds()
		if err != nil {
			atomic.AddUint64(&errors, 1)
			return
		}
		if got != values[i] {
			atomic.AddUint64(&mismatches, 1)
		}
	})
	readDuration := time.Since(readStart)

	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })

	out := result{
		Target:        *target,
		Addr:          *addr,
		Keys:          *keys,
		Workers:       *workers,
		ValueBytes:    logicalBytes,
		LoadSeconds:   loadDuration.Seconds(),
		LoadOpsPerSec: float64(*keys) / loadDuration.Seconds(),
		ReadSeconds:   readDuration.Seconds(),
		ReadOpsPerSec: float64(*keys) / readDuration.Seconds(),
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
	if len(samples) == 0 {
		return 0
	}
	idx := (len(samples) * p) / 100
	if idx >= len(samples) {
		idx = len(samples) - 1
	}
	return samples[idx]
}

func sessionValue(i int) string {
	return fmt.Sprintf(
		`{"id":"%08x-0000-4000-8000-000000000000","country":"LV","plan":"free","status":"active","active":true,"user":%d,"createdAt":"2026-09-07T12:34:56Z","roles":["reader","member"],"permissions":["profile.read","session.refresh","notifications.read"],"preferences":{"language":"lv","timezone":"Europe/Riga","theme":"dark","digest":true},"organization":{"id":"org-00000001","name":"Example Organization","tier":"standard"},"featureFlags":{"newDashboard":true,"auditLog":false,"betaSearch":false},"lastSeen":{"ip":"192.0.2.1","device":"web","region":"eu-north"}}`,
		i, i,
	)
}
