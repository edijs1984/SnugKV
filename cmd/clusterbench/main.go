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
	r    *bufio.Reader
	w    *bufio.Writer
}

func dial(addr string) (*respConn, error) {
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return nil, err
	}
	return &respConn{
		conn: c,
		r:    bufio.NewReaderSize(c, 64<<10),
		w:    bufio.NewWriterSize(c, 64<<10),
	}, nil
}

func (c *respConn) close() { _ = c.conn.Close() }

func (c *respConn) writeCommand(args ...string) error {
	if _, err := fmt.Fprintf(c.w, "*%d\r\n", len(args)); err != nil {
		return err
	}
	for _, arg := range args {
		if _, err := fmt.Fprintf(c.w, "$%d\r\n%s\r\n", len(arg), arg); err != nil {
			return err
		}
	}
	return nil
}

func (c *respConn) flush() error {
	_ = c.conn.SetDeadline(time.Now().Add(10 * time.Second))
	return c.w.Flush()
}

func (c *respConn) readReply() (string, error) {
	_ = c.conn.SetDeadline(time.Now().Add(10 * time.Second))
	return readRESP(c.r)
}

func readRESP(r *bufio.Reader) (string, error) {
	prefix, err := r.ReadByte()
	if err != nil {
		return "", err
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")

	switch prefix {
	case '+', ':':
		return line, nil
	case '-':
		return "", fmt.Errorf("%s", line)
	case '$':
		n, err := strconv.Atoi(line)
		if err != nil {
			return "", err
		}
		if n < 0 {
			return "", nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		if buf[n] != '\r' || buf[n+1] != '\n' {
			return "", fmt.Errorf("invalid bulk terminator")
		}
		return string(buf[:n]), nil
	default:
		return "", fmt.Errorf("unsupported RESP prefix %q", prefix)
	}
}

type workerClient struct {
	conns     map[string]*respConn
	slotCache map[int]string
}

func newWorkerClient() *workerClient {
	return &workerClient{
		conns:     make(map[string]*respConn),
		slotCache: make(map[int]string),
	}
}

func (c *workerClient) close() {
	for _, conn := range c.conns {
		conn.close()
	}
}

func (c *workerClient) get(addr string) (*respConn, error) {
	if conn := c.conns[addr]; conn != nil {
		return conn, nil
	}
	conn, err := dial(addr)
	if err != nil {
		return nil, err
	}
	c.conns[addr] = conn
	return conn, nil
}

func parseMoved(err error) (int, string, bool) {
	if err == nil {
		return 0, "", false
	}
	fields := strings.Fields(err.Error())
	if len(fields) != 3 || fields[0] != "MOVED" {
		return 0, "", false
	}
	slot, parseErr := strconv.Atoi(fields[1])
	if parseErr != nil {
		return 0, "", false
	}
	return slot, fields[2], true
}

func crc16(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func hashKey(key string) string {
	start := strings.IndexByte(key, '{')
	if start < 0 {
		return key
	}
	endRel := strings.IndexByte(key[start+1:], '}')
	if endRel <= 0 {
		return key
	}
	return key[start+1 : start+1+endRel]
}

func slotForKey(key string) int {
	return int(crc16([]byte(hashKey(key))) % 16384)
}

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

func keyFor(i int) string {
	return fmt.Sprintf("clusterbench:key:%09d", i)
}

func makeValue(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + (i % 26))
	}
	return string(b)
}

type batchOp struct {
	slot   int
	addr   string
	target string
	args   []string
}

func buildOp(mode, workload, seed string, nodes []string, client *workerClient, key, value string) batchOp {
	slot := slotForKey(key)
	addr := seed
	switch mode {
	case "direct":
		addr = staticOwner(slot, nodes)
	case "cache":
		if cached := client.slotCache[slot]; cached != "" {
			addr = cached
		}
	}

	args := []string{"GET", key}
	if workload == "set" {
		args = []string{"SET", key, value}
	}
	return batchOp{slot: slot, addr: addr, args: args}
}

func executeBatch(client *workerClient, mode string, ops []batchOp) (redirects int64, errorsCount int64) {
	groups := make(map[string][]int)
	for i := range ops {
		groups[ops[i].addr] = append(groups[ops[i].addr], i)
	}

	redirected := make([]int, 0)

	for addr, indexes := range groups {
		conn, err := client.get(addr)
		if err != nil {
			errorsCount += int64(len(indexes))
			continue
		}

		writeFailed := false
		for _, idx := range indexes {
			if err := conn.writeCommand(ops[idx].args...); err != nil {
				errorsCount += int64(len(indexes))
				writeFailed = true
				break
			}
		}
		if writeFailed {
			continue
		}
		if err := conn.flush(); err != nil {
			errorsCount += int64(len(indexes))
			continue
		}

		for _, idx := range indexes {
			_, err := conn.readReply()
			if err == nil {
				continue
			}
			slot, target, ok := parseMoved(err)
			if !ok {
				errorsCount++
				continue
			}
			redirects++
			ops[idx].slot = slot
			ops[idx].target = target
			if mode == "cache" {
				client.slotCache[slot] = target
			}
			redirected = append(redirected, idx)
		}
	}

	if len(redirected) == 0 {
		return redirects, errorsCount
	}

	redirectGroups := make(map[string][]int)
	for _, idx := range redirected {
		redirectGroups[ops[idx].target] = append(redirectGroups[ops[idx].target], idx)
	}

	for addr, indexes := range redirectGroups {
		conn, err := client.get(addr)
		if err != nil {
			errorsCount += int64(len(indexes))
			continue
		}

		writeFailed := false
		for _, idx := range indexes {
			if err := conn.writeCommand(ops[idx].args...); err != nil {
				errorsCount += int64(len(indexes))
				writeFailed = true
				break
			}
		}
		if writeFailed {
			continue
		}
		if err := conn.flush(); err != nil {
			errorsCount += int64(len(indexes))
			continue
		}

		for range indexes {
			if _, err := conn.readReply(); err != nil {
				errorsCount++
			}
		}
	}

	return redirects, errorsCount
}

func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func main() {
	mode := flag.String("mode", "direct", "direct, redirect, or cache")
	workload := flag.String("workload", "get", "get or set")
	seed := flag.String("seed-addr", "127.0.0.1:7200", "cluster seed address")
	nodesArg := flag.String("nodes", "127.0.0.1:7200,127.0.0.1:7201,127.0.0.1:7202", "three static cluster node addresses")
	keys := flag.Int("keys", 100000, "key cardinality")
	opsCount := flag.Int("ops", 200000, "operations")
	workers := flag.Int("workers", runtime.NumCPU(), "concurrent workers")
	valueBytes := flag.Int("value-bytes", 256, "SET value bytes")
	pipeline := flag.Int("pipeline", 256, "pipeline depth per worker")
	flag.Parse()

	nodes := strings.Split(*nodesArg, ",")
	if len(nodes) != 3 {
		fmt.Fprintln(os.Stderr, "-nodes must contain exactly three comma-separated addresses")
		os.Exit(2)
	}
	if *keys <= 0 || *opsCount <= 0 || *workers <= 0 || *pipeline <= 0 {
		fmt.Fprintln(os.Stderr, "keys, ops, workers, and pipeline must be positive")
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
	var errorsTotal atomic.Int64
	var redirectsTotal atomic.Int64
	samples := make([][]int64, *workers)

	var wg sync.WaitGroup
	start := time.Now()

	for worker := 0; worker < *workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			client := newWorkerClient()
			defer client.close()

			local := make([]int64, 0, (*opsCount/((*workers)*(*pipeline)))+2)
			batch := make([]batchOp, 0, *pipeline)

			for {
				batch = batch[:0]
				for len(batch) < *pipeline {
					n := int(next.Add(1) - 1)
					if n >= *opsCount {
						break
					}
					key := keyFor(n % *keys)
					batch = append(batch, buildOp(*mode, *workload, *seed, nodes, client, key, value))
				}
				if len(batch) == 0 {
					break
				}

				t0 := time.Now()
				redirects, errs := executeBatch(client, *mode, batch)
				elapsed := time.Since(t0)
				redirectsTotal.Add(redirects)
				errorsTotal.Add(errs)

				local = append(local, elapsed.Nanoseconds()/int64(len(batch)))
			}

			samples[worker] = local
		}(worker)
	}

	wg.Wait()
	elapsed := time.Since(start)

	all := make([]int64, 0)
	for _, s := range samples {
		all = append(all, s...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })

	out := map[string]any{
		"mode":           *mode,
		"workload":       *workload,
		"seed_addr":      *seed,
		"nodes":          nodes,
		"keys":           *keys,
		"ops":            *opsCount,
		"workers":        *workers,
		"pipeline":       *pipeline,
		"value_bytes":    *valueBytes,
		"duration_ns":    elapsed.Nanoseconds(),
		"ops_per_second": float64(*opsCount) / elapsed.Seconds(),
		"p50_ns":         percentile(all, 0.50),
		"p95_ns":         percentile(all, 0.95),
		"p99_ns":         percentile(all, 0.99),
		"errors":         errorsTotal.Load(),
		"redirects":      redirectsTotal.Load(),
		"go":             runtime.Version(),
		"os":             runtime.GOOS,
		"arch":           runtime.GOARCH,
		"cpus":           runtime.NumCPU(),
		"measurement_note": "black-box RESP2/TCP pipelined cluster benchmark; latency samples are amortized per-operation batch times; direct uses known static slot owners, redirect always starts at seed and follows MOVED, cache learns per-slot MOVED destinations",
	}
	if len(all) > 0 {
		out["max_ns"] = all[len(all)-1]
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
