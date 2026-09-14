package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

type result struct {
	DurationSeconds float64 `json:"duration_seconds"`
	Workers         int     `json:"workers"`
	Operations      uint64  `json:"operations"`
	Reads           uint64  `json:"reads"`
	Writes          uint64  `json:"writes"`
	Pipelines       uint64  `json:"pipelines"`
	TTLChecks       uint64  `json:"ttl_checks"`
	BinaryChecks    uint64  `json:"binary_checks"`
	Reconnects      uint64  `json:"reconnects"`
	MissingChecks   uint64  `json:"missing_checks"`
	Errors          uint64  `json:"errors"`
	OpsPerSecond    float64 `json:"ops_per_second"`
}

func newClient(addr string, pool int) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr: addr, Protocol: 2, PoolSize: pool,
		DialTimeout: 3 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second,
	})
}

func main() {
	addr := flag.String("addr", "127.0.0.1:6380", "SnugKV address")
	duration := flag.Duration("duration", 10*time.Minute, "duration")
	workers := flag.Int("workers", 8, "workers")
	keys := flag.Int("keys", 256, "keys per worker")
	seed := flag.Int64("seed", 1, "seed")
	flag.Parse()

	if *duration <= 0 || *workers < 1 || *keys < 8 {
		fmt.Fprintln(os.Stderr, "invalid arguments")
		os.Exit(2)
	}

	ctx := context.Background()
	client := newClient(*addr, *workers*2)
	defer client.Close()

	if pong, err := client.Ping(ctx).Result(); err != nil || pong != "PONG" {
		fmt.Fprintf(os.Stderr, "initial PING failed: %q %v\n", pong, err)
		os.Exit(1)
	}

	expected := make([][][]byte, *workers)
	for w := 0; w < *workers; w++ {
		expected[w] = make([][]byte, *keys)
		for k := 0; k < *keys; k++ {
			v := []byte(fmt.Sprintf("w=%d;k=%d;v=0", w, k))
			expected[w][k] = append([]byte(nil), v...)
			if err := client.Set(ctx, fmt.Sprintf("netsoak:%d:%d", w, k), v, 0).Err(); err != nil {
				panic(err)
			}
		}
	}

	start := time.Now()
	deadline := start.Add(*duration)
	var ops, reads, writes, pipes, ttls, binaries, reconnects, missing, failures uint64
	var first sync.Once
	record := func(format string, args ...any) {
		atomic.AddUint64(&failures, 1)
		first.Do(func() { fmt.Fprintf(os.Stderr, "FIRST ERROR: "+format+"\n", args...) })
	}

	var wg sync.WaitGroup
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(*seed + int64(id+1)))
			version := make([]uint64, *keys)

			for time.Now().Before(deadline) {
				k := rng.Intn(*keys)
				key := fmt.Sprintf("netsoak:%d:%d", id, k)
				a := rng.Intn(100)

				switch {
				case a < 60:
					got, err := client.Get(ctx, key).Bytes()
					if err != nil {
						record("GET %s: %v", key, err)
					} else if !bytes.Equal(got, expected[id][k]) {
						record("GET mismatch %s", key)
					}
					atomic.AddUint64(&reads, 1)

				case a < 85:
					version[k]++
					v := []byte(fmt.Sprintf("w=%d;k=%d;v=%d", id, k, version[k]))
					if err := client.Set(ctx, key, v, 0).Err(); err != nil {
						record("SET %s: %v", key, err)
					} else {
						expected[id][k] = append(expected[id][k][:0], v...)
					}
					atomic.AddUint64(&writes, 1)

				case a < 92:
					version[k]++
					v := []byte(fmt.Sprintf("w=%d;k=%d;v=%d", id, k, version[k]))
					p := client.Pipeline()
					p.Set(ctx, key, v, 0)
					g := p.Get(ctx, key)
					_, err := p.Exec(ctx)
					if err != nil {
						record("pipeline %s: %v", key, err)
					} else if g.Val() != string(v) {
						record("pipeline mismatch %s", key)
					} else {
						expected[id][k] = append(expected[id][k][:0], v...)
					}
					atomic.AddUint64(&pipes, 1)
					atomic.AddUint64(&writes, 1)
					atomic.AddUint64(&reads, 1)

				case a < 96:
					tk := fmt.Sprintf("netsoak:ttl:%d:%d", id, k)
					if err := client.Set(ctx, tk, "temporary", 2*time.Second).Err(); err != nil {
						record("TTL SET: %v", err)
					} else if ttl, err := client.PTTL(ctx, tk).Result(); err != nil || ttl <= 0 || ttl > 2*time.Second {
						record("PTTL: %v ttl=%v", err, ttl)
					}
					atomic.AddUint64(&ttls, 1)

				case a < 98:
					bk := fmt.Sprintf("netsoak:bin:%d:%d", id, k)
					v := []byte{0, 1, 2, 13, 10, byte(id), byte(k), 255, 128}
					if err := client.Set(ctx, bk, v, 0).Err(); err != nil {
						record("binary SET: %v", err)
					} else if got, err := client.Get(ctx, bk).Bytes(); err != nil || !bytes.Equal(got, v) {
						record("binary GET: %v", err)
					}
					atomic.AddUint64(&binaries, 1)

				case a < 99:
					tmp := newClient(*addr, 1)
					pong, err := tmp.Ping(ctx).Result()
					tmp.Close()
					if err != nil || pong != "PONG" {
						record("reconnect: %q %v", pong, err)
					}
					atomic.AddUint64(&reconnects, 1)

				default:
					_, err := client.Get(ctx, fmt.Sprintf("netsoak:missing:%d:%d", id, rng.Int63())).Result()
					if !errors.Is(err, redis.Nil) {
						record("missing GET: %v", err)
					}
					atomic.AddUint64(&missing, 1)
				}
				atomic.AddUint64(&ops, 1)
			}
		}(w)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

loop:
	for {
		select {
		case <-ticker.C:
			elapsed := time.Since(start).Seconds()
			n := atomic.LoadUint64(&ops)
			fmt.Fprintf(os.Stderr, "progress elapsed=%s ops=%d ops/s=%.0f errors=%d\n", time.Since(start).Round(time.Second), n, float64(n)/elapsed, atomic.LoadUint64(&failures))
		case <-done:
			break loop
		}
	}

	elapsed := time.Since(start)
	n := atomic.LoadUint64(&ops)
	out := result{
		DurationSeconds: elapsed.Seconds(), Workers: *workers, Operations: n,
		Reads: atomic.LoadUint64(&reads), Writes: atomic.LoadUint64(&writes),
		Pipelines: atomic.LoadUint64(&pipes), TTLChecks: atomic.LoadUint64(&ttls),
		BinaryChecks: atomic.LoadUint64(&binaries), Reconnects: atomic.LoadUint64(&reconnects),
		MissingChecks: atomic.LoadUint64(&missing), Errors: atomic.LoadUint64(&failures),
		OpsPerSecond: float64(n) / elapsed.Seconds(),
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		panic(err)
	}
	if out.Errors != 0 {
		os.Exit(1)
	}
}
