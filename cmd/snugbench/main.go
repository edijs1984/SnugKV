// snugbench measures a deterministic raw engine workload and verifies every read.
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"snugkv/internal/engine"
	"sort"
	"time"
)

func main() {
	count := flag.Int("keys", 10000, "number of keys")
	size := flag.Int("bytes", 256, "value bytes")
	dataset := flag.String("dataset", "random", "random, sessions, telemetry, counters, api, compressible, compressed, mixed")
	encoding := flag.Bool("encoding", false, "enable cheap value encodings")
	jsonShape := flag.Bool("json-shape", false, "enable JSON shape storage")
	compression := flag.Bool("compression", false, "enable general compression candidates")
	seed := flag.Int64("seed", 1, "random seed")
	flag.Parse()
	if *count < 1 || *size < 1 {
		fmt.Fprintln(os.Stderr, "keys and bytes must be positive")
		os.Exit(2)
	}
	s, err := engine.NewWithOptions(engine.Options{
		Shards:        256,
		Encoding:      *encoding,
		ShapeEncoding: *jsonShape,
		Compression:   *compression,
	})
	if err != nil {
		panic(err)
	}
	r := rand.New(rand.NewSource(*seed))
	values := make([][]byte, *count)
	for i := range values {
		switch *dataset {
		case "sessions":
			values[i] = []byte(fmt.Sprintf(`{"id":"%08x-0000-4000-8000-000000000000","country":"LV","plan":"free","status":"active","active":true,"user":%d,"createdAt":"2026-09-07T12:34:56Z","roles":["reader","member"],"permissions":["profile.read","session.refresh","notifications.read"],"preferences":{"language":"lv","timezone":"Europe/Riga","theme":"dark","digest":true},"organization":{"id":"org-00000001","name":"Example Organization","tier":"standard"},"featureFlags":{"newDashboard":true,"auditLog":false,"betaSearch":false},"lastSeen":{"ip":"192.0.2.1","device":"web","region":"eu-north"}}`, i, i))
		case "telemetry":
			values[i] = []byte(fmt.Sprintf(`{"equipment":"sensor-%d","timestamp":"2026-09-07T00:00:00Z","value":%d,"state":"active"}`, i%100, i%1000))
		case "api":
			values[i] = []byte(fmt.Sprintf(`{"status":"ok","requestId":"%08x","items":[{"id":%d,"name":"Example item","enabled":true,"category":"standard","description":"A moderately sized API response record used to measure repeated structural bytes without changing the client-visible JSON representation.","links":{"self":"/v1/items/%d","collection":"/v1/items"},"metadata":{"owner":"team-cache","region":"eu-north","revision":7}}],"paging":{"next":null,"limit":50,"total":1},"generatedAt":"2026-09-07T12:34:56Z"}`, i, i, i))
		case "counters":
			values[i] = []byte(fmt.Sprint(i))
		case "compressed":
			var buffer bytes.Buffer
			writer := gzip.NewWriter(&buffer)
			payload := make([]byte, *size)
			r.Read(payload)
			writer.Write(payload)
			writer.Close()
			values[i] = bytes.Clone(buffer.Bytes())
		case "compressible":
			values[i] = bytes.Repeat([]byte("repeated telemetry payload;"), (*size+26)/27)
			values[i] = values[i][:*size]
		case "random", "mixed":
			values[i] = make([]byte, *size)
			r.Read(values[i])
		default:
			fmt.Fprintln(os.Stderr, "unknown dataset")
			os.Exit(2)
		}
	}
	start := time.Now()
	for i, v := range values {
		if err := s.Set(fmt.Sprint(i), v, 0); err != nil {
			panic(err)
		}
	}
	if *jsonShape || *compression {
		for pass := 0; pass < 10; pass++ {
			for i := range values {
				candidate, ok := s.Candidate(fmt.Sprint(i), 1<<20)
				if ok {
					s.Rewrite(candidate, s.EncodeCandidate(candidate))
				}
			}
		}
		s.Compact(256 << 20)
	}
	load := time.Since(start)
	samples := make([]int64, *count)
	start = time.Now()
	for i, v := range values {
		before := time.Now()
		got, ok := s.Get(fmt.Sprint(i))
		samples[i] = time.Since(before).Nanoseconds()
		if !ok || string(got) != string(v) {
			panic("data mismatch")
		}
	}
	reads := time.Since(start)
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	runtime.GC()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	mode := "raw"
	if *encoding {
		mode = "encoded"
	}
	out := map[string]interface{}{"go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "cpus": runtime.NumCPU(), "gomaxprocs": runtime.GOMAXPROCS(0), "dataset": *dataset, "seed": *seed, "keys": *count, "shards": 256, "mode": mode, "load_ns": load.Nanoseconds(), "reads_ns": reads.Nanoseconds(), "get_p50_ns": samples[len(samples)/2], "get_p95_ns": samples[len(samples)*95/100], "get_p99_ns": samples[len(samples)*99/100], "process_heap_alloc": mem.HeapAlloc, "logical": s.Stats(), "memory": s.Memory(), "inspection": s.Inspect(), "mismatches": 0, "measurement_note": "single run; process heap includes benchmark harness; no product claim"}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		panic(err)
	}
}
