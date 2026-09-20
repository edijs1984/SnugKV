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
	valueShape := flag.String("value-shape", "repetitive", "value shape: random, repetitive, or json")
	pipeline := flag.Int("pipeline", 256, "pipeline depth for load/get")
	seed := flag.Int64("seed", 1, "deterministic seed")
	settleMS := flag.Int("settle-ms", 0, "milliseconds to wait after workload before post-workload memory snapshot")
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
	case "random", "repetitive", "json":
	default:
		fatalf("value-shape must be random, repetitive, or json")
	}
	if *valueShape == "json" && *valueBytes < 32 {
		fatalf("json value-shape requires value-bytes >= 32")
	}
	if *settleMS < 0 {
		fatalf("settle-ms must be non-negative")
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
		elapsed, samples, errs = runLoad(*addr, *keys, *valueBytes, *valueShape, *pipeline, *seed)
	case "get":
		elapsed, samples, errs = runPipelinedGet(*addr, *keys, *ops, *workers, *pipeline, *seed)
	case "get-seq":
		elapsed, samples, errs = runConcurrent(*addr, "get", *keys, *ops, *workers, *valueBytes, *valueShape, *seed)
	case "mixed", "ttl":
		elapsed, samples, errs = runConcurrent(*addr, *workload, *keys, *ops, *workers, *valueBytes, *valueShape, *seed)
	}

	if *settleMS > 0 {
		time.Sleep(time.Duration(*settleMS) * time.Millisecond)
	}

	control, err = dial(*addr)
	if err != nil { fatalf("reconnect after workload: %v", err) }
	defer control.Close()

	after, err := control.usedMemory()
	if err != nil { fatalf("INFO memory after: %v", err) }
	dbsize, err := control.dbsize()
	if err != nil { fatalf("DBSIZE: %v", err) }

	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	measuredOps := *ops
	if *workload == "load" { measuredOps = *keys }

	delta := uint64(0)
	if after >= before { delta = after - before }
	bytesPerKey := float64(0)
	if *workload == "load" { bytesPerKey = float64(delta)/float64(*keys) }

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
		"duration_ns": elapsed.Nanoseconds(),
		"ops_per_second": float64(measuredOps)/elapsed.Seconds(),
		"p50_ns": pct(samples,50),
		"p95_ns": pct(samples,95),
		"p99_ns": pct(samples,99),
		"max_ns": pct(samples,100),
		"used_memory_before": before,
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

func runLoad(addr string, keys, valueBytes int, valueShape string, pipeline int, seed int64) (time.Duration, []int64, uint64) {
	c, err := dial(addr)
	if err != nil { fatalf("load connect: %v", err) }
	defer c.Close()
	samples := make([]int64,0,(keys+pipeline-1)/pipeline)
	var errs uint64
	start := time.Now()
	for base:=0;base<keys;base+=pipeline {
		end:=base+pipeline
		if end>keys { end=keys }
		batchStart:=time.Now()
		for i:=base;i<end;i++ {
			value := benchmarkValue(valueShape, valueBytes, i, seed)
			if err:=c.write(b("SET"),key(i),value);err!=nil { fatalf("SET write: %v",err) }
		}
		if err:=c.w.Flush();err!=nil { fatalf("SET flush: %v",err) }
		for i:=base;i<end;i++ {
			line,err:=c.readLine()
			if err!=nil { fatalf("SET reply: %v",err) }
			if string(line)!="+OK" { errs++ }
		}
		samples=append(samples,time.Since(batchStart).Nanoseconds()/int64(end-base))
	}
	return time.Since(start),samples,errs
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
		prefix := []byte(fmt.Sprintf("{\"id\":%d,\"name\":\"user-%d\",\"message\":\"", keyIndex, keyIndex))
		suffix := []byte("\"}")
		if len(prefix)+len(suffix) > size {
			v := append([]byte(nil), prefix...)
			v = append(v, suffix...)
			return v[:size]
		}
		v := make([]byte, 0, size)
		v = append(v, prefix...)
		for len(v)+len(suffix) < size {
			v = append(v, byte('a'+(keyIndex+len(v))%23))
		}
		v = append(v, suffix...)
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
	if line[0]!='

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

func measurementNote(workload string, pipeline int) string {
	if workload == "get" {
		return fmt.Sprintf("black-box RESP2/TCP pipelined GET (depth=%d); percentile samples are amortized per-op batch times; use multiple repetitions before product claims", pipeline)
	}
	if workload == "get-seq" {
		return "black-box RESP2/TCP sequential GET; use multiple repetitions before product claims"
	}
	return "black-box RESP2/TCP single run; use multiple repetitions before product claims"
}

func fatalf(format string,args ...any){
	fmt.Fprintf(os.Stderr,"rediswirebench: "+format+"\n",args...)
	os.Exit(1)
}
{return fmt.Errorf("unexpected GET reply %q",line)}
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

func fatalf(format string,args ...any){
	fmt.Fprintf(os.Stderr,"rediswirebench: "+format+"\n",args...)
	os.Exit(1)
}
