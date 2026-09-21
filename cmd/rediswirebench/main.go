// rediswirebench benchmarks a Redis-compatible server over RESP2/TCP.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type client struct {
	conn net.Conn
	r *bufio.Reader
	w *bufio.Writer
}

func main() {
	server := flag.String("server", "server", "label for JSON output")
	addr := flag.String("addr", "127.0.0.1:6379", "server address")
	workload := flag.String("workload", "load", "load, get, get-seq, mixed, ttl")
	keys := flag.Int("keys", 1000000, "dataset key count")
	ops := flag.Int("ops", 1000000, "operations for get/mixed/ttl")
	workers := flag.Int("workers", runtime.NumCPU(), "concurrent workers")
	valueBytes := flag.Int("value-bytes", 64, "value bytes")
	valueShape := flag.String("value-shape", "repetitive", "value shape: random, repetitive, json, session-json, api-json, cache-json, counter, uuid, text, or compressed")
	pipeline := flag.Int("pipeline", 256, "pipeline depth for load/get")
	seed := flag.Int64("seed", 1, "deterministic seed")
	settleMS := flag.Int("settle-ms", 0, "milliseconds to wait after workload before final memory snapshot")
	convergeMS := flag.Int("converge-ms", 0, "maximum milliseconds to wait for memory convergence after settle; 0 disables")
	convergePollMS := flag.Int("converge-poll-ms", 1000, "memory convergence polling interval in milliseconds")
	reset := flag.Bool("reset", false, "FLUSHDB before workload")
	cleanup := flag.Bool("cleanup", false, "FLUSHDB after workload")
	flag.Parse()

	if *keys < 1 || *ops < 1 || *workers < 1 || *valueBytes < 1 || *pipeline < 1 {
		fatalf("keys, ops, workers, value-bytes and pipeline must be positive")
	}
	switch *workload {
	case "load", "get", "get-seq", "mixed", "ttl":
	default:
		fatalf("workload must be load, get, get-seq, mixed, or ttl")
	}
	switch *valueShape {
	case "random", "repetitive", "json", "session-json", "api-json", "cache-json", "counter", "uuid", "text", "compressed":
	default:
		fatalf("unsupported value-shape %q", *valueShape)
	}
	if (*valueShape == "json" || *valueShape == "session-json" || *valueShape == "api-json" || *valueShape == "cache-json") && *valueBytes < 64 {
		fatalf("%s value-shape requires value-bytes >= 64", *valueShape)
	}
	if *valueShape == "counter" && *valueBytes != 10 {
		fatalf("counter value-shape requires value-bytes=10")
	}
	if *valueShape == "uuid" && *valueBytes != 36 {
		fatalf("uuid value-shape requires value-bytes=36")
	}
	if *valueShape == "compressed" && *valueBytes < 16 {
		fatalf("compressed value-shape requires value-bytes >= 16")
	}
	if *settleMS < 0 || *convergeMS < 0 || *convergePollMS < 100 {
		fatalf("settle-ms/converge-ms must be non-negative and converge-poll-ms >= 100")
	}

	control, err := dial(*addr)
	if err != nil { fatalf("connect: %v", err) }

	if *reset {
		if err := control.expectSimple("OK", b("FLUSHDB")); err != nil {
			control.Close()
			fatalf("FLUSHDB: %v", err)
		}
	}

	before, err := control.usedMemory()
	if err != nil {
		control.Close()
		fatalf("INFO memory before: %v", err)
	}
	control.Close()

	var elapsed time.Duration
	var samples []int64
	var errs uint64
	switch *workload {
	case "load":
		elapsed, samples, errs = runLoad(*addr, *keys, *workers, *valueBytes, *valueShape, *pipeline, *seed)
	case "get":
		elapsed, samples, errs = runPipelinedGet(*addr, *keys, *ops, *workers, *pipeline, *seed)
	case "get-seq":
		elapsed, samples, errs = runConcurrent(*addr, "get", *keys, *ops, *workers, *valueBytes, *valueShape, *seed)
	case "mixed", "ttl":
		elapsed, samples, errs = runConcurrent(*addr, *workload, *keys, *ops, *workers, *valueBytes, *valueShape, *seed)
	}

	control, err = dial(*addr)
	if err != nil { fatalf("reconnect after workload: %v", err) }

	postWorkload, err := control.usedMemory()
	if err != nil {
		control.Close()
		fatalf("INFO memory post-workload: %v", err)
	}
	control.Close()

	if *settleMS > 0 {
		time.Sleep(time.Duration(*settleMS) * time.Millisecond)
	}

	after := postWorkload
	converged := false
	convergenceMS := int64(0)
	convergenceSamples := 0
	if *convergeMS > 0 {
		after, converged, convergenceMS, convergenceSamples, err = waitForMemoryConvergence(
			*addr,
			time.Duration(*convergeMS)*time.Millisecond,
			time.Duration(*convergePollMS)*time.Millisecond,
		)
		if err != nil {
			fatalf("memory convergence: %v", err)
		}
	} else {
		control, err = dial(*addr)
		if err != nil { fatalf("reconnect after settle: %v", err) }
		after, err = control.usedMemory()
		control.Close()
		if err != nil { fatalf("INFO memory after: %v", err) }
	}

	control, err = dial(*addr)
	if err != nil { fatalf("reconnect for DBSIZE: %v", err) }
	defer control.Close()
	dbsize, err := control.dbsize()
	if err != nil { fatalf("DBSIZE: %v", err) }

	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	measuredOps := *ops
	if *workload == "load" { measuredOps = *keys }

	postDelta := uint64(0)
	if postWorkload >= before { postDelta = postWorkload - before }
	delta := uint64(0)
	if after >= before { delta = after - before }
	postBytesPerKey := float64(0)
	bytesPerKey := float64(0)
	if *workload == "load" {
		postBytesPerKey = float64(postDelta)/float64(*keys)
		bytesPerKey = float64(delta)/float64(*keys)
	}

	out := map[string]any{
		"server": *server,
		"addr": *addr,
		"workload": *workload,
		"keys": *keys,
		"ops": measuredOps,
		"workers": *workers,
		"value_bytes": *valueBytes,
		"value_shape": *valueShape,
		"pipeline": *pipeline,
		"seed": *seed,
		"settle_ms": *settleMS,
		"converge_ms": *convergeMS,
		"convergence_poll_ms": *convergePollMS,
		"converged": converged,
		"convergence_elapsed_ms": convergenceMS,
		"convergence_samples": convergenceSamples,
		"duration_ns": elapsed.Nanoseconds(),
		"ops_per_second": float64(measuredOps)/elapsed.Seconds(),
		"p50_ns": pct(samples,50),
		"p95_ns": pct(samples,95),
		"p99_ns": pct(samples,99),
		"max_ns": pct(samples,100),
		"used_memory_before": before,
		"used_memory_post_workload": postWorkload,
		"used_memory_post_workload_delta": postDelta,
		"bytes_per_key_post_workload": postBytesPerKey,
		"used_memory_after": after,
		"used_memory_delta": delta,
		"bytes_per_key_delta": bytesPerKey,
		"dbsize_after": dbsize,
		"errors": errs,
		"go": runtime.Version(),
		"os": runtime.GOOS,
		"arch": runtime.GOARCH,
		"cpus": runtime.NumCPU(),
		"measurement_note": measurementNote(*workload, *pipeline),
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil { fatalf("encode: %v", err) }

	if *cleanup {
		if err := control.expectSimple("OK", b("FLUSHDB")); err != nil { fatalf("cleanup: %v", err) }
	}
	if errs != 0 { os.Exit(1) }
}

func runLoad(addr string, keys, workers, valueBytes int, valueShape string, pipeline int, seed int64) (time.Duration, []int64, uint64) {
	samples := make([]int64, keys)
	var next uint64
	var errs uint64
	var wg sync.WaitGroup
	start := time.Now()

	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			c, err := dial(addr)
			if err != nil {
				atomic.AddUint64(&errs, 1)
				return
			}
			defer c.Close()

			for {
				base := int(atomic.AddUint64(&next, uint64(pipeline)) - uint64(pipeline))
				if base >= keys {
					return
				}

				end := base + pipeline
				if end > keys {
					end = keys
				}

				batchStart := time.Now()
				for i := base; i < end; i++ {
					value := benchmarkValue(valueShape, valueBytes, i, seed)
					if err := c.write(b("SET"), key(i), value); err != nil {
						atomic.AddUint64(&errs, uint64(end-i))
						return
					}
				}

				if err := c.w.Flush(); err != nil {
					atomic.AddUint64(&errs, uint64(end-base))
					return
				}

				for i := base; i < end; i++ {
					line, err := c.readLine()
					if err != nil {
						atomic.AddUint64(&errs, uint64(end-i))
						return
					}
					if string(line) != "+OK" {
						atomic.AddUint64(&errs, 1)
					}
				}

				perOp := time.Since(batchStart).Nanoseconds() / int64(end-base)
				for i := base; i < end; i++ {
					samples[i] = perOp
				}
			}
		}(worker)
	}

	wg.Wait()
	return time.Since(start), samples, errs
}

func runPipelinedGet(addr string, keys, ops, workers, pipeline int, seed int64) (time.Duration, []int64, uint64) {
	samples := make([]int64, ops)
	var next uint64
	var errs uint64
	var wg sync.WaitGroup
	start := time.Now()

	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			c, err := dial(addr)
			if err != nil {
				atomic.AddUint64(&errs, 1)
				return
			}
			defer c.Close()

			rng := rand.New(rand.NewSource(seed + int64(id+1)*1000003))
			for {
				base := int(atomic.AddUint64(&next, uint64(pipeline)) - uint64(pipeline))
				if base >= ops {
					return
				}
				end := base + pipeline
				if end > ops {
					end = ops
				}

				batchStart := time.Now()
				for i := base; i < end; i++ {
					k := rng.Intn(keys)
					if err := c.write(b("GET"), key(k)); err != nil {
						atomic.AddUint64(&errs, uint64(end-i))
						return
					}
				}
				if err := c.w.Flush(); err != nil {
					atomic.AddUint64(&errs, uint64(end-base))
					return
				}
				for i := base; i < end; i++ {
					if err := c.readGetReply(); err != nil {
						atomic.AddUint64(&errs, 1)
					}
				}

				perOp := time.Since(batchStart).Nanoseconds() / int64(end-base)
				for i := base; i < end; i++ {
					samples[i] = perOp
				}
			}
		}(worker)
	}
	wg.Wait()
	return time.Since(start), samples, errs
}

func runConcurrent(addr, workload string, keys, ops, workers, valueBytes int, valueShape string, seed int64) (time.Duration, []int64, uint64) {
	samples:=make([]int64,ops)
	var next uint64
	var errs uint64
	var wg sync.WaitGroup
	start:=time.Now()
	for worker:=0;worker<workers;worker++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			c,err:=dial(addr)
			if err!=nil { atomic.AddUint64(&errs,1);return }
			defer c.Close()
			rng:=rand.New(rand.NewSource(seed+int64(id+1)*1000003))
			for {
				idx:=int(atomic.AddUint64(&next,1)-1)
				if idx>=ops { return }
				k:=rng.Intn(keys)
				begin:=time.Now()
				var opErr error
				switch workload {
				case "get":
					opErr=c.get(key(k))
				case "mixed":
					if rng.Intn(10)==0 {
						opErr=c.set(key(k),benchmarkValue(valueShape,valueBytes,k,seed))
					} else {
						opErr=c.get(key(k))
					}
				case "ttl":
					opErr=c.setPX(key(k),benchmarkValue(valueShape,valueBytes,k,seed),60000)
				}
				samples[idx]=time.Since(begin).Nanoseconds()
				if opErr!=nil { atomic.AddUint64(&errs,1) }
			}
		}(worker)
	}
	wg.Wait()
	return time.Since(start),samples,errs
}

func pct(sorted []int64,p int) int64 {
	if len(sorted)==0 { return 0 }
	if p>=100 { return sorted[len(sorted)-1] }
	idx:=(len(sorted)*p+99)/100
	if idx<1 { idx=1 }
	if idx>len(sorted) { idx=len(sorted) }
	return sorted[idx-1]
}

func benchmarkValue(shape string, size, keyIndex int, seed int64) []byte {
	switch shape {
	case "random":
		v := make([]byte, size)
		x := uint64(seed) ^ uint64(keyIndex+1)*0x9e3779b97f4a7c15
		for i := range v {
			x ^= x << 13
			x ^= x >> 7
			x ^= x << 17
			v[i] = byte(x)
		}
		return v

	case "json":
		return paddedJSON(size,
			fmt.Sprintf("{\"id\":%d,\"name\":\"user-%d\",\"message\":\"", keyIndex, keyIndex),
			"\"}",
			keyIndex,
		)

	case "session-json":
		return paddedJSON(size,
			fmt.Sprintf("{\"user_id\":%d,\"role\":\"user\",\"authenticated\":true,\"expires_in\":3600,\"csrf\":\"%08x\",\"state\":\"", keyIndex, uint32(uint64(seed)^uint64(keyIndex)*2654435761)),
			"\"}",
			keyIndex+17,
		)

	case "api-json":
		return paddedJSON(size,
			fmt.Sprintf("{\"id\":%d,\"status\":\"ok\",\"page\":%d,\"cached\":true,\"items\":[{\"sku\":\"SKU-%06d\",\"qty\":1}],\"payload\":\"", keyIndex, keyIndex%100, keyIndex%1000000),
			"\"}",
			keyIndex+31,
		)

	case "cache-json":
		// Typical application cache entry: request identity + cached response
		// metadata + nested response data. Field names repeat across entries while
		// IDs, paths, etags and payload content vary per key.
		return paddedJSON(size,
			fmt.Sprintf("{\"cache_key\":\"GET:/api/v1/users/%d\",\"request\":{\"method\":\"GET\",\"path\":\"/api/v1/users/%d\",\"query\":{\"include\":\"profile,settings\"},\"tenant_id\":%d,\"locale\":\"en\"},\"response\":{\"status\":200,\"content_type\":\"application/json\",\"etag\":\"%08x\",\"data\":{\"user\":{\"id\":%d,\"plan\":\"pro\",\"active\":true},\"permissions\":[\"read\",\"write\"],\"payload\":\"", keyIndex, keyIndex, keyIndex%10000, uint32(uint64(seed)^uint64(keyIndex)*2654435761), keyIndex),
			"\"}},\"cached_at\":\"2026-09-21T08:00:00Z\",\"ttl\":300}",
			keyIndex+53,
		)

	case "counter":
		// Keep a canonical ten-byte integer so SnugKV's integer codec and Redis's
		// normal string representation see a realistic counter workload.
		return []byte(strconv.FormatInt(1_000_000_000+int64(keyIndex%1_000_000_000), 10))

	case "uuid":
		x := uint64(seed) ^ uint64(keyIndex+1)*0x9e3779b97f4a7c15
		y := x ^ 0xd6e8feb86659fd93
		return []byte(fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
			uint32(x>>32),
			uint16(x>>16),
			uint16(x),
			uint16(y>>48),
			y&0x0000ffffffffffff,
		))

	case "text":
		prefix := []byte(fmt.Sprintf("user %d cached response: ", keyIndex))
		v := make([]byte, 0, size)
		v = append(v, prefix...)
		words := []byte("profile settings dashboard notifications preferences ")
		for len(v) < size {
			remain := size - len(v)
			if remain >= len(words) {
				v = append(v, words...)
			} else {
				v = append(v, words[:remain]...)
			}
		}
		return v

	case "compressed":
		// Simulate an already-compressed/binary payload. The gzip signature lets
		// SnugKV's compressed-data detector avoid futile recompression while the
		// remaining bytes are deterministic high entropy.
		v := make([]byte, size)
		v[0], v[1] = 0x1f, 0x8b
		x := uint64(seed) ^ uint64(keyIndex+1)*0x9e3779b97f4a7c15
		for i := 2; i < len(v); i++ {
			x ^= x << 13
			x ^= x >> 7
			x ^= x << 17
			v[i] = byte(x)
		}
		return v

	default:
		v := make([]byte,size)
		pattern := []byte("snugkv-benchmark-")
		for i:=range v {
			v[i]=pattern[(i+keyIndex)%len(pattern)]
		}
		return v
	}
}
func paddedJSON(size int, prefix, suffix string, salt int) []byte {
	p := []byte(prefix)
	s := []byte(suffix)
	if len(p)+len(s) > size {
		panic("benchmark JSON template exceeds requested size")
	}
	v := make([]byte, 0, size)
	v = append(v, p...)
	for len(v)+len(s) < size {
		v = append(v, byte('a'+(salt+len(v))%23))
	}
	v = append(v, s...)
	return v
}

func key(i int) []byte { return []byte(fmt.Sprintf("bench:%09d",i)) }
func b(s string) []byte { return []byte(s) }

func dial(addr string)(*client,error){
	conn,err:=net.DialTimeout("tcp",addr,5*time.Second)
	if err!=nil{return nil,err}
	return &client{
		conn:conn,
		r:bufio.NewReaderSize(conn,256<<10),
		w:bufio.NewWriterSize(conn,256<<10),
	},nil
}
func(c *client)Close()error{return c.conn.Close()}

func(c *client)write(args ...[]byte)error{
	if _,err:=fmt.Fprintf(c.w,"*%d\r\n",len(args));err!=nil{return err}
	for _,arg:=range args{
		if _,err:=fmt.Fprintf(c.w,"$%d\r\n",len(arg));err!=nil{return err}
		if _,err:=c.w.Write(arg);err!=nil{return err}
		if _,err:=c.w.WriteString("\r\n");err!=nil{return err}
	}
	return nil
}

func(c *client)readLine()([]byte,error){
	line,err:=c.r.ReadBytes('\n')
	if err!=nil{return nil,err}
	if len(line)<2||line[len(line)-2]!='\r'{return nil,errors.New("invalid RESP line")}
	return line[:len(line)-2],nil
}

func(c *client)expectSimple(want string,args ...[]byte)error{
	if err:=c.write(args...);err!=nil{return err}
	if err:=c.w.Flush();err!=nil{return err}
	line,err:=c.readLine()
	if err!=nil{return err}
	if len(line)==0{return io.ErrUnexpectedEOF}
	if line[0]=='-'{return errors.New(string(line[1:]))}
	if string(line)!="+"+want{return fmt.Errorf("unexpected reply %q",line)}
	return nil
}
func(c *client)set(k,v []byte)error{return c.expectSimple("OK",b("SET"),k,v)}
func(c *client)setPX(k,v []byte,ttl int)error{return c.expectSimple("OK",b("SET"),k,v,b("PX"),[]byte(strconv.Itoa(ttl)))}

func(c *client)get(k []byte)error{
	if err:=c.write(b("GET"),k);err!=nil{return err}
	if err:=c.w.Flush();err!=nil{return err}
	return c.readGetReply()
}

func(c *client)readGetReply()error{
	line,err:=c.readLine()
	if err!=nil{return err}
	if len(line)==0{return io.ErrUnexpectedEOF}
	if line[0]=='-'{return errors.New(string(line[1:]))}
	if line[0]!='$'{return fmt.Errorf("unexpected GET reply %q",line)}
	n,err:=strconv.Atoi(string(line[1:]))
	if err!=nil{return err}
	if n<0{return nil}
	payload:=make([]byte,n+2)
	if _,err:=io.ReadFull(c.r,payload);err!=nil{return err}
	if !bytes.Equal(payload[n:],[]byte("\r\n")){return errors.New("invalid bulk terminator")}
	return nil
}

func(c *client)usedMemory()(uint64,error){
	if err:=c.write(b("INFO"),b("memory"));err!=nil{return 0,err}
	if err:=c.w.Flush();err!=nil{return 0,err}
	payload,err:=c.readBulk()
	if err!=nil{return 0,err}
	for _,line:=range strings.Split(string(payload),"\r\n"){
		if strings.HasPrefix(line,"used_memory:"){return strconv.ParseUint(strings.TrimPrefix(line,"used_memory:"),10,64)}
	}
	return 0,errors.New("used_memory missing")
}

func(c *client)dbsize()(int64,error){
	if err:=c.write(b("DBSIZE"));err!=nil{return 0,err}
	if err:=c.w.Flush();err!=nil{return 0,err}
	line,err:=c.readLine()
	if err!=nil{return 0,err}
	if len(line)==0||line[0]!=':'{return 0,fmt.Errorf("unexpected DBSIZE reply %q",line)}
	return strconv.ParseInt(string(line[1:]),10,64)
}

func(c *client)readBulk()([]byte,error){
	line,err:=c.readLine()
	if err!=nil{return nil,err}
	if len(line)==0{return nil,io.ErrUnexpectedEOF}
	if line[0]=='-'{return nil,errors.New(string(line[1:]))}
	if line[0]!='$'{return nil,fmt.Errorf("unexpected bulk reply %q",line)}
	n,err:=strconv.Atoi(string(line[1:]))
	if err!=nil||n<0{return nil,fmt.Errorf("invalid bulk length %q",line)}
	payload:=make([]byte,n+2)
	if _,err:=io.ReadFull(c.r,payload);err!=nil{return nil,err}
	if !bytes.Equal(payload[n:],[]byte("\r\n")){return nil,errors.New("invalid bulk terminator")}
	return payload[:n],nil
}

type convergenceProgress struct {
	ElapsedMS          int64  `json:"elapsed_ms"`
	UsedMemory         uint64 `json:"used_memory"`
	OptimizerRewritten uint64 `json:"optimizer_rewritten,omitempty"`
	OptimizerQueue     int    `json:"optimizer_queue_depth,omitempty"`
	ArenaBytes         uint64 `json:"arena_bytes,omitempty"`
	ArenaPayloadBytes  uint64 `json:"arena_payload_bytes,omitempty"`
}

func emitConvergenceProgress(p convergenceProgress) {
	data, err := json.Marshal(p)
	if err != nil {
		return
	}
	fmt.Fprintf(os.Stderr, "BENCH_PROGRESS %s\n", data)
}

func parseSnugStats(payload []byte) map[string]uint64 {
	out := make(map[string]uint64)
	for _, line := range strings.Split(string(payload), "\n") {
		name, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(value, 10, 64)
		if err == nil {
			out[name] = n
		}
	}
	return out
}

func (c *client) snugStats() (map[string]uint64, error) {
	if err := c.write(b("SNUG.STATS")); err != nil {
		return nil, err
	}
	if err := c.w.Flush(); err != nil {
		return nil, err
	}
	payload, err := c.readBulk()
	if err != nil {
		return nil, err
	}
	return parseSnugStats(payload), nil
}

func waitForMemoryConvergence(addr string, maxWait, poll time.Duration) (uint64, bool, int64, int, error) {
	start := time.Now()
	deadline := start.Add(maxWait)

	// Compaction maintenance runs on a ten-second cadence. Require memory to
	// remain effectively unchanged for at least 12 seconds so we do not report
	// a transient optimizer plateau just before a compaction pass.
	stableFor := 12 * time.Second
	var (
		anchor     uint64
		haveAnchor bool
		lastChange = start
		samples    int
	)

	for {
		c, err := dial(addr)
		if err != nil {
			return 0, false, time.Since(start).Milliseconds(), samples, err
		}
		used, err := c.usedMemory()
		var snug map[string]uint64
		if err == nil {
			// Convergence is used by SnugKV optimized runs. Keep SNUG.STATS
			// optional so rediswirebench remains usable against ordinary Redis.
			snug, _ = c.snugStats()
		}
		c.Close()
		if err != nil {
			return 0, false, time.Since(start).Milliseconds(), samples, err
		}
		samples++

		now := time.Now()
		progress := convergenceProgress{
			ElapsedMS:  now.Sub(start).Milliseconds(),
			UsedMemory: used,
		}
		if snug != nil {
			progress.OptimizerRewritten = snug["optimizer_rewritten"]
			progress.OptimizerQueue = int(snug["optimizer_queue_depth"])
			progress.ArenaBytes = snug["arena_bytes"]
			progress.ArenaPayloadBytes = snug["arena_payload_bytes"]
		}
		emitConvergenceProgress(progress)
		if !haveAnchor {
			anchor = used
			haveAnchor = true
			lastChange = now
		} else {
			// Treat cumulative movement of at least 0.1% or 256 KiB as
			// meaningful. Comparing with a stable anchor means a sequence of
			// individually small reductions still resets the convergence clock
			// once their combined effect becomes material.
			threshold := anchor / 1000
			if threshold < 256<<10 {
				threshold = 256 << 10
			}
			var movement uint64
			if used >= anchor {
				movement = used - anchor
			} else {
				movement = anchor - used
			}
			if movement >= threshold {
				anchor = used
				lastChange = now
			}
		}

		if now.Sub(lastChange) >= stableFor {
			return used, true, now.Sub(start).Milliseconds(), samples, nil
		}
		if !now.Before(deadline) {
			return used, false, now.Sub(start).Milliseconds(), samples, nil
		}
		time.Sleep(poll)
	}
}

func measurementNote(workload string, pipeline int) string {
	if workload == "get" {
		return fmt.Sprintf("black-box RESP2/TCP pipelined GET (depth=%d); percentile samples are amortized per-op batch times; use multiple repetitions before product claims", pipeline)
	}
	if workload == "get-seq" {
		return "black-box RESP2/TCP sequential GET; use multiple repetitions before product claims"
	}
	if workload == "load" {
		return fmt.Sprintf("black-box RESP2/TCP pipelined SET (depth=%d) with concurrent workers; percentile samples are amortized per-op batch times; use multiple repetitions before product claims", pipeline)
	}
	return "black-box RESP2/TCP single run; use multiple repetitions before product claims"
}

func fatalf(format string,args ...any){
	fmt.Fprintf(os.Stderr,"rediswirebench: "+format+"\n",args...)
	os.Exit(1)
}
