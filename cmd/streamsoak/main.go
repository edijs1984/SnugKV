// Command streamsoak checks that SnugKV streams behave as a message bus between
// microservices: one publisher, several services, several instances of each
// service, consumers that crash without acknowledging, and a server that is
// killed with SIGKILL and restarted from its AOF while all of that runs.
//
//	streamsoak -bin ./snugkv -messages 20000 -services 2 -instances 2 -kills 4
//
// It passes when every acknowledged message is in the stream after the last
// restart and every service has processed every message at least once.
// Redeliveries after a crash or a lost acknowledgement are counted, not failures.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type config struct {
	addr      string
	bin       string
	dir       string
	messages  int
	services  int
	instances int
	rate      int
	size      int
	kills     int
	crashPct  int
	ramTotal  int
	timeout   time.Duration
	seed      int64
}

const topic = "soak:orders"

// ---- minimal RESP client ----

type respError string

func (e respError) Error() string { return string(e) }

type client struct {
	conn net.Conn
	r    *bufio.Reader
}

func dial(addr string) (*client, error) {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return nil, err
	}
	return &client{conn: c, r: bufio.NewReaderSize(c, 64<<10)}, nil
}

func (c *client) close() { c.conn.Close() }

func (c *client) do(timeout time.Duration, args ...string) (any, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	c.conn.SetDeadline(time.Now().Add(timeout))
	if _, err := io.WriteString(c.conn, b.String()); err != nil {
		return nil, err
	}
	v, err := c.read()
	if err != nil {
		return nil, err
	}
	if e, ok := v.(respError); ok {
		return nil, e
	}
	return v, nil
}

func (c *client) read() (any, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if len(line) < 3 {
		return nil, errors.New("short reply")
	}
	body := line[1 : len(line)-2]
	switch line[0] {
	case '+':
		return body, nil
	case '-':
		return respError(body), nil
	case ':':
		n, _ := strconv.ParseInt(body, 10, 64)
		return n, nil
	case '$':
		n, _ := strconv.Atoi(body)
		if n < 0 {
			return nil, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(c.r, buf); err != nil {
			return nil, err
		}
		return string(buf[:n]), nil
	case '*':
		n, _ := strconv.Atoi(body)
		if n < 0 {
			return nil, nil
		}
		out := make([]any, n)
		for i := range out {
			if out[i], err = c.read(); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("unexpected reply %q", line)
}

// ---- server under test ----

type server struct {
	cfg  config
	mu   sync.Mutex
	cmd  *exec.Cmd
	logf *os.File
	port string
}

func (s *server) args() []string {
	return []string{
		"-listen", s.cfg.addr, "-admin-listen", "127.0.0.1:" + s.port,
		"-aof", filepath.Join(s.cfg.dir, "appendonly.snug"), "-fsync", "always",
		"-stream-dir", filepath.Join(s.cfg.dir, "streams"),
		"-stream-ram-bytes", "262144", "-stream-ram-total", strconv.Itoa(s.cfg.ramTotal),
	}
}

func (s *server) start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cmd := exec.Command(s.cfg.bin, s.args()...)
	cmd.Stdout, cmd.Stderr = s.logf, s.logf
	if err := cmd.Start(); err != nil {
		return err
	}
	s.cmd = cmd
	go cmd.Wait()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := dial(s.cfg.addr); err == nil {
			_, err := c.do(2*time.Second, "PING")
			c.close()
			if err == nil {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("server did not come up")
}

func (s *server) kill() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Signal(syscall.SIGKILL)
		time.Sleep(100 * time.Millisecond)
	}
}

// ---- workload ----

type stats struct {
	published    atomic.Int64
	publishRetry atomic.Int64
	deliveries   atomic.Int64
	crashes      atomic.Int64
	reclaimed    atomic.Int64
	reconnects   atomic.Int64
}

type service struct {
	name string
	mu   sync.Mutex
	seen map[int]int // seq -> times processed
}

func (sv *service) record(seq int) (first bool) {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	sv.seen[seq]++
	return sv.seen[seq] == 1
}

func (sv *service) unique() int {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	return len(sv.seen)
}

func entrySeq(entry any) (id string, seq int, ok bool) {
	parts, _ := entry.([]any)
	if len(parts) != 2 {
		return "", 0, false
	}
	id, _ = parts[0].(string)
	fields, _ := parts[1].([]any)
	for i := 0; i+1 < len(fields); i += 2 {
		if k, _ := fields[i].(string); k == "seq" {
			v, _ := fields[i+1].(string)
			n, err := strconv.Atoi(v)
			return id, n, err == nil
		}
	}
	return id, 0, false
}

// entries flattens an XREADGROUP/XAUTOCLAIM-style reply into stream entries.
func readEntries(reply any) []any {
	streams, _ := reply.([]any)
	var out []any
	for _, st := range streams {
		pair, _ := st.([]any)
		if len(pair) == 2 {
			list, _ := pair[1].([]any)
			out = append(out, list...)
		}
	}
	return out
}

func publisher(cfg config, st *stats, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	payload := strings.Repeat("x", cfg.size)
	var c *client
	interval := time.Duration(0)
	if cfg.rate > 0 {
		interval = time.Second / time.Duration(cfg.rate)
	}
	next := time.Now()
	for seq := 1; seq <= cfg.messages; seq++ {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if c == nil {
				var err error
				if c, err = dial(cfg.addr); err != nil {
					time.Sleep(100 * time.Millisecond)
					continue
				}
			}
			if _, err := c.do(5*time.Second, "XADD", topic, "*", "seq", strconv.Itoa(seq), "p", payload); err != nil {
				// The server may have stored it before dying; sending it again
				// can create a duplicate, which services must tolerate.
				c.close()
				c = nil
				st.publishRetry.Add(1)
				time.Sleep(100 * time.Millisecond)
				continue
			}
			st.published.Add(1)
			break
		}
		if interval > 0 {
			next = next.Add(interval)
			if d := time.Until(next); d > 0 {
				time.Sleep(d)
			}
		}
	}
}

func consumer(cfg config, st *stats, sv *service, id int, stop <-chan struct{}, rng *rand.Rand) {
	name := fmt.Sprintf("%s-%d", sv.name, id)
	var c *client
	var backlogFirst = true // after a (re)connect, first read our own unacknowledged messages
	for {
		select {
		case <-stop:
			if c != nil {
				c.close()
			}
			return
		default:
		}
		if c == nil {
			var err error
			if c, err = dial(cfg.addr); err != nil {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			st.reconnects.Add(1)
			c.do(5*time.Second, "XGROUP", "CREATE", topic, sv.name, "0", "MKSTREAM") // BUSYGROUP is fine
			backlogFirst = true
		}
		var reply any
		var err error
		cursor := ">"
		if backlogFirst {
			cursor = "0"
		}
		reply, err = c.do(5*time.Second, "XREADGROUP", "GROUP", sv.name, name, "COUNT", "25", "BLOCK", "300", "STREAMS", topic, cursor)
		if err != nil {
			c.close()
			c = nil
			time.Sleep(50 * time.Millisecond)
			continue
		}
		list := readEntries(reply)
		if backlogFirst && len(list) == 0 {
			backlogFirst = false
			// Take over what a crashed instance of this service left behind.
			if r, err := c.do(5*time.Second, "XAUTOCLAIM", topic, sv.name, name, "1500", "0-0", "COUNT", "50"); err == nil {
				if parts, _ := r.([]any); len(parts) >= 2 {
					claimed, _ := parts[1].([]any)
					st.reclaimed.Add(int64(len(claimed)))
					list = claimed
				}
			}
		}
		var ids []string
		for _, e := range list {
			eid, seq, ok := entrySeq(e)
			if !ok {
				continue
			}
			sv.record(seq)
			st.deliveries.Add(1)
			ids = append(ids, eid)
		}
		if len(ids) == 0 {
			continue
		}
		if cfg.crashPct > 0 && rng.Intn(100) < cfg.crashPct {
			// Crash after processing, before acknowledging.
			st.crashes.Add(1)
			c.close()
			c = nil
			continue
		}
		args := append([]string{"XACK", topic, sv.name}, ids...)
		if _, err := c.do(5*time.Second, args...); err != nil {
			c.close()
			c = nil
		}
	}
}

func chaos(cfg config, srv *server, stop <-chan struct{}, kills *atomic.Int64, rng *rand.Rand, pubDone <-chan struct{}) {
	for k := 0; k < cfg.kills; k++ {
		select {
		case <-stop:
			return
		case <-pubDone:
			return
		case <-time.After(time.Duration(1500+rng.Intn(2500)) * time.Millisecond):
		}
		srv.kill()
		time.Sleep(time.Duration(200+rng.Intn(600)) * time.Millisecond)
		if err := srv.start(); err != nil {
			fmt.Fprintln(os.Stderr, "restart failed:", err)
			return
		}
		kills.Add(1)
	}
}

// storedSeqs reads the whole stream back and returns the distinct seq values.
func storedSeqs(addr string) (map[int]bool, int, error) {
	c, err := dial(addr)
	if err != nil {
		return nil, 0, err
	}
	defer c.close()
	seqs := map[int]bool{}
	total := 0
	start := "-"
	for {
		r, err := c.do(30*time.Second, "XRANGE", topic, start, "+", "COUNT", "2000")
		if err != nil {
			return nil, 0, err
		}
		list, _ := r.([]any)
		if len(list) == 0 {
			break
		}
		var last string
		for _, e := range list {
			id, seq, ok := entrySeq(e)
			if ok {
				seqs[seq] = true
			}
			total++
			last = id
		}
		if len(list) < 2000 {
			break
		}
		start = "(" + last
	}
	return seqs, total, nil
}

type report struct {
	Messages        int            `json:"messages"`
	Services        int            `json:"services"`
	Instances       int            `json:"instances_per_service"`
	ServerKills     int64          `json:"server_kills"`
	ConsumerCrashes int64          `json:"consumer_crashes_before_ack"`
	PublishRetries  int64          `json:"publish_retries"`
	Reconnects      int64          `json:"consumer_reconnects"`
	Reclaimed       int64          `json:"messages_taken_over_from_crashed_instances"`
	StoredEntries   int            `json:"stream_entries_stored"`
	StoredDistinct  int            `json:"stream_distinct_messages"`
	LostAfterAck    int            `json:"acknowledged_messages_lost"`
	Deliveries      int64          `json:"deliveries"`
	Redelivered     int64          `json:"redeliveries"`
	MissingByStream map[string]int `json:"messages_never_processed_by_service"`
	PendingLeft     map[string]int `json:"unacknowledged_left_by_service"`
	Seconds         float64        `json:"seconds"`
	Pass            bool           `json:"pass"`
	Problems        []string       `json:"problems,omitempty"`
}

func main() {
	var cfg config
	flag.StringVar(&cfg.addr, "addr", "127.0.0.1:16399", "server address")
	flag.StringVar(&cfg.bin, "bin", "", "snugkv binary; the soak starts it, kills it and restarts it")
	flag.StringVar(&cfg.dir, "dir", "", "data directory for the AOF and stream segments (default: a temporary directory)")
	flag.IntVar(&cfg.messages, "messages", 20000, "messages to publish")
	flag.IntVar(&cfg.services, "services", 2, "services, each reading every message")
	flag.IntVar(&cfg.instances, "instances", 2, "instances of each service, sharing its messages")
	flag.IntVar(&cfg.rate, "rate", 4000, "publish rate per second, zero for as fast as possible")
	flag.IntVar(&cfg.size, "size", 512, "payload bytes")
	flag.IntVar(&cfg.kills, "kills", 4, "times to kill -9 and restart the server (needs -bin)")
	flag.IntVar(&cfg.crashPct, "crash-pct", 3, "percent of batches an instance drops without acknowledging")
	flag.IntVar(&cfg.ramTotal, "stream-ram-total", 1<<20, "memory budget for streams on the server under test")
	flag.DurationVar(&cfg.timeout, "timeout", 3*time.Minute, "give up after this long")
	flag.Int64Var(&cfg.seed, "seed", time.Now().UnixNano(), "random seed")
	flag.Parse()

	if cfg.bin == "" && cfg.kills > 0 {
		fmt.Fprintln(os.Stderr, "-bin is required to kill and restart the server; use -kills 0 against an existing server")
		os.Exit(2)
	}
	rng := rand.New(rand.NewSource(cfg.seed))
	started := time.Now()
	var srv *server
	if cfg.bin != "" {
		if cfg.dir == "" {
			d, err := os.MkdirTemp("", "streamsoak-")
			if err != nil {
				panic(err)
			}
			cfg.dir = d
			defer os.RemoveAll(d)
		}
		os.MkdirAll(cfg.dir, 0o755)
		logf, _ := os.Create(filepath.Join(cfg.dir, "server.log"))
		srv = &server{cfg: cfg, logf: logf, port: strconv.Itoa(16400 + rng.Intn(500))}
		if err := srv.start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer srv.kill()
	}

	// Groups exist before the first message so no service misses the start.
	boot, err := dial(cfg.addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	svcs := make([]*service, cfg.services)
	for i := range svcs {
		svcs[i] = &service{name: fmt.Sprintf("svc%d", i+1), seen: map[int]int{}}
		boot.do(5*time.Second, "XGROUP", "CREATE", topic, svcs[i].name, "0", "MKSTREAM")
	}
	boot.close()

	var st stats
	var kills atomic.Int64
	stop := make(chan struct{})
	pubDone := make(chan struct{})
	var wg sync.WaitGroup
	for _, sv := range svcs {
		for i := 1; i <= cfg.instances; i++ {
			wg.Add(1)
			go func(sv *service, i int, r *rand.Rand) {
				defer wg.Done()
				consumer(cfg, &st, sv, i, stop, r)
			}(sv, i, rand.New(rand.NewSource(rng.Int63())))
		}
	}
	go publisher(cfg, &st, stop, pubDone)
	if srv != nil && cfg.kills > 0 {
		go chaos(cfg, srv, stop, &kills, rand.New(rand.NewSource(rng.Int63())), pubDone)
	}

	var problems []string
	<-pubDone
	// Wait for every service to process every message.
	deadline := started.Add(cfg.timeout)
	for time.Now().Before(deadline) {
		done := true
		for _, sv := range svcs {
			if sv.unique() < cfg.messages {
				done = false
			}
		}
		if done {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Let acknowledgements land, then stop the consumers.
	time.Sleep(2 * time.Second)
	close(stop)
	wg.Wait()

	// One more crash and restart, then read the stream back from the AOF.
	if srv != nil {
		srv.kill()
		time.Sleep(300 * time.Millisecond)
		if err := srv.start(); err != nil {
			problems = append(problems, "final restart failed: "+err.Error())
		} else {
			kills.Add(1)
		}
	}
	seqs, total, err := storedSeqs(cfg.addr)
	if err != nil {
		problems = append(problems, "reading the stream back: "+err.Error())
	}
	lost := 0
	for seq := 1; seq <= cfg.messages; seq++ {
		if !seqs[seq] {
			lost++
		}
	}
	if lost > 0 {
		problems = append(problems, fmt.Sprintf("%d acknowledged messages are missing after restart", lost))
	}

	rep := report{
		Messages: cfg.messages, Services: cfg.services, Instances: cfg.instances,
		ServerKills: kills.Load(), ConsumerCrashes: st.crashes.Load(), PublishRetries: st.publishRetry.Load(),
		Reconnects: st.reconnects.Load(), Reclaimed: st.reclaimed.Load(),
		StoredEntries: total, StoredDistinct: len(seqs), LostAfterAck: lost,
		Deliveries:      st.deliveries.Load(),
		MissingByStream: map[string]int{}, PendingLeft: map[string]int{},
	}
	for _, sv := range svcs {
		missing := 0
		for seq := 1; seq <= cfg.messages; seq++ {
			if sv.seen[seq] == 0 {
				missing++
			}
		}
		rep.MissingByStream[sv.name] = missing
		if missing > 0 {
			problems = append(problems, fmt.Sprintf("%s never processed %d messages", sv.name, missing))
		}
	}
	rep.Redelivered = rep.Deliveries - int64(cfg.messages*cfg.services)
	if c, err := dial(cfg.addr); err == nil {
		if r, err := c.do(5*time.Second, "XINFO", "GROUPS", topic); err == nil {
			groups, _ := r.([]any)
			for _, g := range groups {
				fields, _ := g.([]any)
				var name string
				var pending int
				for i := 0; i+1 < len(fields); i += 2 {
					switch fields[i] {
					case "name":
						name, _ = fields[i+1].(string)
					case "pending":
						if n, ok := fields[i+1].(int64); ok {
							pending = int(n)
						}
					}
				}
				rep.PendingLeft[name] = pending
			}
		}
		c.close()
	}
	rep.Seconds = time.Since(started).Seconds()
	rep.Problems = problems
	rep.Pass = len(problems) == 0
	out, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Println(string(out))
	if !rep.Pass {
		os.Exit(1)
	}
}
