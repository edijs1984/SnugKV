// redisstructurebench benchmarks native Redis data structures over RESP2/TCP.
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
	mode := flag.String("mode", "load", "load or read")
	dataType := flag.String("type", "hash", "hash, list, set, or zset")
	items := flag.Int("items", 1000000, "total logical elements")
	cardinality := flag.Int("cardinality", 10, "elements per container key")
	ops := flag.Int("ops", 2000000, "read operations")
	workers := flag.Int("workers", runtime.NumCPU(), "concurrent workers")
	pipeline := flag.Int("pipeline", 256, "pipeline depth")
	valueBytes := flag.Int("value-bytes", 64, "hash/list value size")
	seed := flag.Int64("seed", 1, "deterministic seed")
	convergeMS := flag.Int("converge-ms", 0, "wait for memory convergence after load; -1 waits indefinitely")
	reset := flag.Bool("reset", false, "FLUSHDB before workload")
	flag.Parse()

	if *items < 1 || *cardinality < 1 || *ops < 1 || *workers < 1 || *pipeline < 1 || *valueBytes < 1 {
		fatalf("items, cardinality, ops, workers, pipeline and value-bytes must be positive")
	}
	if *items < *cardinality {
		fatalf("items must be >= cardinality")
	}
	switch *mode {
	case "load", "read":
	default:
		fatalf("mode must be load or read")
	}
	switch *dataType {
	case "hash", "list", "set", "zset":
	default:
		fatalf("type must be hash, list, set, or zset")
	}
	if *convergeMS < -1 {
		fatalf("converge-ms must be >= -1")
	}

	containers := (*items + *cardinality - 1) / *cardinality
	control, err := dial(*addr)
	if err != nil { fatalf("connect: %v", err) }
	if *reset {
		if err := control.expectSimple("OK", b("FLUSHDB")); err != nil {
			control.Close()
			fatalf("FLUSHDB: %v", err)
		}
	}
	before, err := control.usedMemory()
	if err != nil { control.Close(); fatalf("INFO memory before: %v", err) }
	control.Close()

	var elapsed time.Duration
	var samples []int64
	var errs uint64
	if *mode == "load" {
		elapsed, samples, errs = runLoad(*addr, *dataType, *items, *cardinality, *workers, *pipeline, *valueBytes, *seed)
	} else {
		elapsed, samples, errs = runRead(*addr, *dataType, *items, *cardinality, *ops, *workers, *pipeline, *seed)
	}

	control, err = dial(*addr)
	if err != nil { fatalf("reconnect: %v", err) }
	postWorkload, err := control.usedMemory()
	control.Close()
	if err != nil { fatalf("INFO memory post-workload: %v", err) }

	after := postWorkload
	converged := false
	convergenceElapsed := int64(0)
	convergenceSamples := 0
	if *mode == "load" && *convergeMS != 0 {
		after, converged, convergenceElapsed, convergenceSamples, err = waitForMemoryConvergence(*addr, *convergeMS)
		if err != nil { fatalf("memory convergence: %v", err) }
	}

	control, err = dial(*addr)
	if err != nil { fatalf("reconnect for DBSIZE: %v", err) }
	dbsize, err := control.dbsize()
	control.Close()
	if err != nil { fatalf("DBSIZE: %v", err) }

	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	measuredOps := *ops
	if *mode == "load" { measuredOps = *items }

	postDelta := uint64(0)
	if postWorkload >= before { postDelta = postWorkload - before }
	delta := uint64(0)
	if after >= before { delta = after - before }
	postBytesPerItem := float64(0)
	bytesPerItem := float64(0)
	if *mode == "load" {
		postBytesPerItem = float64(postDelta) / float64(*items)
		bytesPerItem = float64(delta) / float64(*items)
	}

	out := map[string]any{
		"server": *server,
		"addr": *addr,
		"workload": *mode,
		"data_type": *dataType,
		"logical_unit": "item",
		"logical_items": *items,
		"container_keys": containers,
		"cardinality": *cardinality,
		"keys": *items,
		"ops": measuredOps,
		"workers": *workers,
		"pipeline": *pipeline,
		"value_bytes": *valueBytes,
		"value_shape": fmt.Sprintf("%s-%d", *dataType, *cardinality),
		"seed": *seed,
		"converge_ms": *convergeMS,
		"converged": converged,
		"convergence_elapsed_ms": convergenceElapsed,
		"convergence_samples": convergenceSamples,
		"duration_ns": elapsed.Nanoseconds(),
		"ops_per_second": float64(measuredOps) / elapsed.Seconds(),
		"p50_ns": pct(samples, 50),
		"p95_ns": pct(samples, 95),
		"p99_ns": pct(samples, 99),
		"max_ns": pct(samples, 100),
		"used_memory_before": before,
		"used_memory_post_workload": postWorkload,
		"used_memory_post_workload_delta": postDelta,
		"bytes_per_key_post_workload": postBytesPerItem,
		"bytes_per_item_post_workload": postBytesPerItem,
		"used_memory_after": after,
		"used_memory_delta": delta,
		"bytes_per_key_delta": bytesPerItem,
		"bytes_per_item": bytesPerItem,
		"dbsize_after": dbsize,
		"errors": errs,
		"go": runtime.Version(),
		"os": runtime.GOOS,
		"arch": runtime.GOARCH,
		"cpus": runtime.NumCPU(),
		"measurement_note": fmt.Sprintf("black-box RESP2/TCP pipelined %s benchmark for %s cardinality=%d; memory is bytes per logical item", *mode, *dataType, *cardinality),
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil { fatalf("encode: %v", err) }
	if errs != 0 { os.Exit(1) }
}

func runLoad(addr, dataType string, items, cardinality, workers, pipeline, valueBytes int, seed int64) (time.Duration, []int64, uint64) {
	samples := make([]int64, items)
	var next uint64
	var errs uint64
	var wg sync.WaitGroup
	start := time.Now()

	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := dial(addr)
			if err != nil { atomic.AddUint64(&errs, 1); return }
			defer c.Close()
			for {
				base := int(atomic.AddUint64(&next, uint64(pipeline)) - uint64(pipeline))
				if base >= items { return }
				end := base + pipeline
				if end > items { end = items }
				batchStart := time.Now()
				for i := base; i < end; i++ {
					if err := writeLoad(c, dataType, i, cardinality, valueBytes, seed); err != nil {
						atomic.AddUint64(&errs, uint64(end-i)); return
					}
				}
				if err := c.w.Flush(); err != nil { atomic.AddUint64(&errs, uint64(end-base)); return }
				for i := base; i < end; i++ {
					if err := c.readIntegerReply(); err != nil { atomic.AddUint64(&errs, 1) }
				}
				perOp := time.Since(batchStart).Nanoseconds() / int64(end-base)
				for i := base; i < end; i++ { samples[i] = perOp }
			}
		}()
	}
	wg.Wait()
	return time.Since(start), samples, errs
}

func runRead(addr, dataType string, items, cardinality, ops, workers, pipeline int, seed int64) (time.Duration, []int64, uint64) {
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
			if err != nil { atomic.AddUint64(&errs, 1); return }
			defer c.Close()
			rng := rand.New(rand.NewSource(seed + int64(id+1)*1000003))
			for {
				base := int(atomic.AddUint64(&next, uint64(pipeline)) - uint64(pipeline))
				if base >= ops { return }
				end := base + pipeline
				if end > ops { end = ops }
				indexes := make([]int, end-base)
				batchStart := time.Now()
				for i := base; i < end; i++ {
					idx := rng.Intn(items)
					indexes[i-base] = idx
					if err := writeRead(c, dataType, idx, cardinality, seed); err != nil {
						atomic.AddUint64(&errs, uint64(end-i)); return
					}
				}
				if err := c.w.Flush(); err != nil { atomic.AddUint64(&errs, uint64(end-base)); return }
				for range indexes {
					var err error
					if dataType == "set" { err = c.readIntegerReply() } else { err = c.readBulkOrNilReply() }
					if err != nil { atomic.AddUint64(&errs, 1) }
				}
				perOp := time.Since(batchStart).Nanoseconds() / int64(end-base)
				for i := base; i < end; i++ { samples[i] = perOp }
			}
		}(worker)
	}
	wg.Wait()
	return time.Since(start), samples, errs
}

func writeLoad(c *client, dataType string, idx, cardinality, valueBytes int, seed int64) error {
	container := idx / cardinality
	member := idx % cardinality
	k := structureKey(dataType, container)
	switch dataType {
	case "hash":
		return c.write(b("HSET"), k, field(member), benchmarkValue(valueBytes, idx, seed))
	case "list":
		return c.write(b("RPUSH"), k, benchmarkValue(valueBytes, idx, seed))
	case "set":
		return c.write(b("SADD"), k, memberValue(member, idx, seed))
	case "zset":
		return c.write(b("ZADD"), k, []byte(strconv.Itoa(idx)), memberValue(member, idx, seed))
	}
	return errors.New("unsupported type")
}

func writeRead(c *client, dataType string, idx, cardinality int, seed int64) error {
	container := idx / cardinality
	member := idx % cardinality
	k := structureKey(dataType, container)
	switch dataType {
	case "hash":
		return c.write(b("HGET"), k, field(member))
	case "list":
		return c.write(b("LINDEX"), k, []byte(strconv.Itoa(member)))
	case "set":
		return c.write(b("SISMEMBER"), k, memberValue(member, idx, seed))
	case "zset":
		return c.write(b("ZSCORE"), k, memberValue(member, idx, seed))
	}
	return errors.New("unsupported type")
}

func structureKey(dataType string, i int) []byte { return []byte(fmt.Sprintf("bench:%s:%09d", dataType, i)) }
func field(i int) []byte { return []byte(fmt.Sprintf("f:%06d", i)) }

func memberValue(member, idx int, seed int64) []byte {
	x := uint64(seed) ^ uint64(idx+1)*0x9e3779b97f4a7c15
	return []byte(fmt.Sprintf("m:%06d:%016x", member, x))
}

func benchmarkValue(size, idx int, seed int64) []byte {
	prefix := []byte(fmt.Sprintf("value:%09d:", idx))
	v := make([]byte, size)
	copy(v, prefix)
	x := uint64(seed) ^ uint64(idx+1)*0x9e3779b97f4a7c15
	for i := len(prefix); i < len(v); i++ {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		v[i] = byte('a' + x%23)
	}
	return v
}

func waitForMemoryConvergence(addr string, maxMS int) (uint64, bool, int64, int, error) {
	start := time.Now()
	var deadline time.Time
	if maxMS > 0 { deadline = start.Add(time.Duration(maxMS) * time.Millisecond) }
	const stableFor = 12 * time.Second
	var anchor uint64
	var lastChange time.Time
	samples := 0
	for {
		c, err := dial(addr)
		if err != nil { return 0, false, time.Since(start).Milliseconds(), samples, err }
		used, err := c.usedMemory()
		c.Close()
		if err != nil { return 0, false, time.Since(start).Milliseconds(), samples, err }
		samples++
		now := time.Now()
		if anchor == 0 {
			anchor = used
			lastChange = now
		} else {
			threshold := anchor / 1000
			if threshold < 256<<10 { threshold = 256 << 10 }
			var movement uint64
			if used >= anchor { movement = used-anchor } else { movement = anchor-used }
			if movement >= threshold {
				anchor = used
				lastChange = now
			}
		}
		if now.Sub(lastChange) >= stableFor {
			return used, true, now.Sub(start).Milliseconds(), samples, nil
		}
		if !deadline.IsZero() && !now.Before(deadline) {
			return used, false, now.Sub(start).Milliseconds(), samples, nil
		}
		time.Sleep(time.Second)
	}
}

func pct(sorted []int64, p int) int64 {
	if len(sorted)==0 { return 0 }
	if p>=100 { return sorted[len(sorted)-1] }
	idx := (len(sorted)*p+99)/100
	if idx<1 { idx=1 }
	if idx>len(sorted) { idx=len(sorted) }
	return sorted[idx-1]
}

func b(s string) []byte { return []byte(s) }

func dial(addr string) (*client,error) {
	conn,err:=net.DialTimeout("tcp",addr,5*time.Second)
	if err!=nil{return nil,err}
	return &client{conn:conn,r:bufio.NewReaderSize(conn,256<<10),w:bufio.NewWriterSize(conn,256<<10)},nil
}
func (c *client) Close() error { return c.conn.Close() }

func (c *client) write(args ...[]byte) error {
	if _,err:=fmt.Fprintf(c.w,"*%d\r\n",len(args));err!=nil{return err}
	for _,arg:=range args{
		if _,err:=fmt.Fprintf(c.w,"$%d\r\n",len(arg));err!=nil{return err}
		if _,err:=c.w.Write(arg);err!=nil{return err}
		if _,err:=c.w.WriteString("\r\n");err!=nil{return err}
	}
	return nil
}

func (c *client) readLine() ([]byte,error) {
	line,err:=c.r.ReadBytes('\n')
	if err!=nil{return nil,err}
	if len(line)<2||line[len(line)-2]!='\r'{return nil,errors.New("invalid RESP line")}
	return line[:len(line)-2],nil
}

func (c *client) expectSimple(want string,args ...[]byte) error {
	if err:=c.write(args...);err!=nil{return err}
	if err:=c.w.Flush();err!=nil{return err}
	line,err:=c.readLine()
	if err!=nil{return err}
	if len(line)==0{return io.ErrUnexpectedEOF}
	if line[0]=='-'{return errors.New(string(line[1:]))}
	if string(line)!="+"+want{return fmt.Errorf("unexpected reply %q",line)}
	return nil
}

func (c *client) readIntegerReply() error {
	line,err:=c.readLine()
	if err!=nil{return err}
	if len(line)==0{return io.ErrUnexpectedEOF}
	if line[0]=='-'{return errors.New(string(line[1:]))}
	if line[0]!=':'{return fmt.Errorf("unexpected integer reply %q",line)}
	_,err=strconv.ParseInt(string(line[1:]),10,64)
	return err
}

func (c *client) readBulkOrNilReply() error {
	line,err:=c.readLine()
	if err!=nil{return err}
	if len(line)==0{return io.ErrUnexpectedEOF}
	if line[0]=='-'{return errors.New(string(line[1:]))}
	if line[0]!='$'{return fmt.Errorf("unexpected bulk reply %q",line)}
	n,err:=strconv.Atoi(string(line[1:]))
	if err!=nil{return err}
	if n<0{return nil}
	payload:=make([]byte,n+2)
	if _,err:=io.ReadFull(c.r,payload);err!=nil{return err}
	if !bytes.Equal(payload[n:],[]byte("\r\n")){return errors.New("invalid bulk terminator")}
	return nil
}

func (c *client) readBulk() ([]byte,error) {
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

func (c *client) usedMemory() (uint64,error) {
	if err:=c.write(b("INFO"),b("memory"));err!=nil{return 0,err}
	if err:=c.w.Flush();err!=nil{return 0,err}
	payload,err:=c.readBulk()
	if err!=nil{return 0,err}
	for _,line:=range strings.Split(string(payload),"\r\n"){
		if strings.HasPrefix(line,"used_memory:"){return strconv.ParseUint(strings.TrimPrefix(line,"used_memory:"),10,64)}
	}
	return 0,errors.New("used_memory missing")
}

func (c *client) dbsize() (int64,error) {
	if err:=c.write(b("DBSIZE"));err!=nil{return 0,err}
	if err:=c.w.Flush();err!=nil{return 0,err}
	line,err:=c.readLine()
	if err!=nil{return 0,err}
	if len(line)==0||line[0]!=':'{return 0,fmt.Errorf("unexpected DBSIZE reply %q",line)}
	return strconv.ParseInt(string(line[1:]),10,64)
}

func fatalf(format string,args ...any){
	fmt.Fprintf(os.Stderr,"redisstructurebench: "+format+"\n",args...)
	os.Exit(1)
}
