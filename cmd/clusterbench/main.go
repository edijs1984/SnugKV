package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
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

type respConn struct {
	conn net.Conn
	r *bufio.Reader
	w *bufio.Writer
}

func dial(addr string) (*respConn, error) {
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return nil, err
	}
	return &respConn{conn:c, r:bufio.NewReaderSize(c, 64<<10), w:bufio.NewWriterSize(c, 64<<10)}, nil
}

func (c *respConn) close() { _ = c.conn.Close() }

func (c *respConn) command(args ...string) (string, error) {
	_ = c.conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(c.w, "*%d\r\n", len(args)); err != nil { return "", err }
	for _, arg := range args {
		if _, err := fmt.Fprintf(c.w, "$%d\r\n%s\r\n", len(arg), arg); err != nil { return "", err }
	}
	if err := c.w.Flush(); err != nil { return "", err }
	return readRESP(c.r)
}

func readRESP(r *bufio.Reader) (string, error) {
	prefix, err := r.ReadByte()
	if err != nil { return "", err }
	line, err := r.ReadString('\n')
	if err != nil { return "", err }
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	switch prefix {
	case '+', ':':
		return line, nil
	case '-':
		return "", fmt.Errorf("%s", line)
	case '$':
		n, err := strconv.Atoi(line)
		if err != nil { return "", err }
		if n < 0 { return "", nil }
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil { return "", err }
		if buf[n] != '\r' || buf[n+1] != '\n' { return "", fmt.Errorf("invalid bulk terminator") }
		return string(buf[:n]), nil
	default:
		return "", fmt.Errorf("unsupported RESP prefix %q", prefix)
	}
}

type workerClient struct {
	conns map[string]*respConn
	slotCache map[int]string
}

func newWorkerClient() *workerClient {
	return &workerClient{conns:make(map[string]*respConn), slotCache:make(map[int]string)}
}

func (c *workerClient) close() {
	for _, conn := range c.conns { conn.close() }
}

func (c *workerClient) get(addr string) (*respConn, error) {
	if conn := c.conns[addr]; conn != nil { return conn, nil }
	conn, err := dial(addr)
	if err != nil { return nil, err }
	c.conns[addr] = conn
	return conn, nil
}

func parseMoved(err error) (int, string, bool) {
	if err == nil { return 0, "", false }
	fields := strings.Fields(err.Error())
	if len(fields) != 3 || fields[0] != "MOVED" { return 0, "", false }
	slot, parseErr := strconv.Atoi(fields[1])
	if parseErr != nil { return 0, "", false }
	return slot, fields[2], true
}

func crc16(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i:=0; i<8; i++ {
			if crc&0x8000 != 0 { crc = (crc << 1) ^ 0x1021 } else { crc <<= 1 }
		}
	}
	return crc
}

func hashKey(key string) string {
	start := strings.IndexByte(key, '{')
	if start < 0 { return key }
	endRel := strings.IndexByte(key[start+1:], '}')
	if endRel <= 0 { return key }
	return key[start+1:start+1+endRel]
}

func slotForKey(key string) int { return int(crc16([]byte(hashKey(key))) % 16384) }

func staticOwner(slot int, nodes []string) string {
	switch {
	case slot <= 5460:
		return nodes[0]
	case slot <= 10922:
		return nodes[1]
	default:
		return nodes[2]
	}
}

func keyFor(i int) string { return fmt.Sprintf("clusterbench:key:%09d", i) }

func makeValue(n int) string {
	if n <= 0 { return "" }
	b := make([]byte, n)
	for i := range b { b[i] = byte('a' + (i % 26)) }
	return string(b)
}

func doOp(client *workerClient, mode, workload, seed string, nodes []string, key, value string) (int64, error) {
	slot := slotForKey(key)
	addr := seed
	switch mode {
	case "direct":
		addr = staticOwner(slot, nodes)
	case "cache":
		if cached := client.slotCache[slot]; cached != "" { addr = cached }
	case "redirect":
		addr = seed
	default:
		return 0, fmt.Errorf("unknown mode %q", mode)
	}

	args := []string{"GET", key}
	if workload == "set" { args = []string{"SET", key, value} }

	conn, err := client.get(addr)
	if err != nil { return 0, err }
	_, err = conn.command(args...)
	if err == nil { return 0, nil }

	movedSlot, target, ok := parseMoved(err)
	if !ok { return 0, err }
	if mode == "cache" { client.slotCache[movedSlot] = target }
	targetConn, dialErr := client.get(target)
	if dialErr != nil { return 1, dialErr }
	_, err = targetConn.command(args...)
	return 1, err
}

func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 { return 0 }
	idx := int(float64(len(sorted)-1) * p)
	if idx < 0 { idx = 0 }
	if idx >= len(sorted) { idx = len(sorted)-1 }
	return sorted[idx]
}

func main() {
	mode := flag.String("mode", "direct", "direct, redirect, or cache")
	workload := flag.String("workload", "get", "get or set")
	seed := flag.String("seed-addr", "127.0.0.1:7200", "cluster seed address")
	nodesArg := flag.String("nodes", "127.0.0.1:7200,127.0.0.1:7201,127.0.0.1:7202", "three static cluster node addresses")
	keys := flag.Int("keys", 100000, "key cardinality")
	ops := flag.Int("ops", 200000, "operations")
	workers := flag.Int("workers", runtime.NumCPU(), "concurrent workers")
	valueBytes := flag.Int("value-bytes", 256, "SET value bytes")
	flag.Parse()

	nodes := strings.Split(*nodesArg, ",")
	if len(nodes) != 3 {
		fmt.Fprintln(os.Stderr, "-nodes must contain exactly three comma-separated addresses")
		os.Exit(2)
	}
	if *keys <= 0 || *ops <= 0 || *workers <= 0 {
		fmt.Fprintln(os.Stderr, "keys, ops, and workers must be positive")
		os.Exit(2)
	}
	if *workload != "get" && *workload != "set" {
		fmt.Fprintln(os.Stderr, "-workload must be get or set")
		os.Exit(2)
	}
	if *mode != "direct" && *mode != "redirect" && *mode != "cache" {
		fmt.Fprintln(os.Stderr, "-mode must be direct, redirect, or cache")
		os.Exit(2)
	}

	value := makeValue(*valueBytes)
	var next atomic.Int64
	var errorsCount atomic.Int64
	var redirects atomic.Int64
	samples := make([][]int64, *workers)
	var wg sync.WaitGroup

	start := time.Now()
	for worker := 0; worker < *workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			client := newWorkerClient()
			defer client.close()
			local := make([]int64, 0, (*ops / *workers)+1)
			for {
				n := int(next.Add(1) - 1)
				if n >= *ops { break }
				key := keyFor(n % *keys)
				t0 := time.Now()
				r, err := doOp(client, *mode, *workload, *seed, nodes, key, value)
				local = append(local, time.Since(t0).Nanoseconds())
				redirects.Add(r)
				if err != nil { errorsCount.Add(1) }
			}
			samples[worker] = local
		}(worker)
	}
	wg.Wait()
	elapsed := time.Since(start)

	all := make([]int64, 0, *ops)
	for _, s := range samples { all = append(all, s...) }
	sort.Slice(all, func(i,j int) bool { return all[i] < all[j] })

	out := map[string]any{
		"mode": *mode,
		"workload": *workload,
		"seed_addr": *seed,
		"nodes": nodes,
		"keys": *keys,
		"ops": *ops,
		"workers": *workers,
		"value_bytes": *valueBytes,
		"duration_ns": elapsed.Nanoseconds(),
		"ops_per_second": float64(*ops)/elapsed.Seconds(),
		"p50_ns": percentile(all,0.50),
		"p95_ns": percentile(all,0.95),
		"p99_ns": percentile(all,0.99),
		"errors": errorsCount.Load(),
		"redirects": redirects.Load(),
		"go": runtime.Version(),
		"os": runtime.GOOS,
		"arch": runtime.GOARCH,
		"cpus": runtime.NumCPU(),
		"measurement_note": "black-box RESP2/TCP cluster benchmark; direct uses known static slot owners, redirect always starts at seed and follows MOVED, cache learns per-slot MOVED destinations",
	}
	if len(all)>0 { out["max_ns"]=all[len(all)-1] }

	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
