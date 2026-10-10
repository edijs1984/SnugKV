// Command pubsubbench measures Pub/Sub fan-out against any Redis-compatible
// server: delivery rate and latency to healthy subscribers, publisher latency,
// and what the server does with subscribers that stop reading.
//
//	pubsubbench -addr 127.0.0.1:6383 -subs 200 -channels 10 -rate 500 -duration 15s -stuck 5
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type config struct {
	addr       string
	subs       int
	stuck      int
	channels   int
	publishers int
	size       int
	rate       int
	duration   time.Duration
	drain      time.Duration
	pattern    bool
}

type result struct {
	Server              string  `json:"server"`
	Subscribers         int     `json:"subscribers"`
	Channels            int     `json:"channels"`
	Publishers          int     `json:"publishers"`
	Pattern             bool    `json:"pattern"`
	PayloadBytes        int     `json:"payload_bytes"`
	TargetRate          int     `json:"target_publish_rate"`
	Seconds             float64 `json:"seconds"`
	Published           int64   `json:"published"`
	PublishPerSec       float64 `json:"publish_per_sec"`
	PublishP50Ms        float64 `json:"publish_p50_ms"`
	PublishP99Ms        float64 `json:"publish_p99_ms"`
	PublishMaxMs        float64 `json:"publish_max_ms"`
	Expected            int64   `json:"deliveries_expected"`
	Delivered           int64   `json:"deliveries"`
	DeliveredShare      float64 `json:"delivered_share"`
	DeliveriesPerSec    float64 `json:"deliveries_per_sec"`
	DeliveryP50Ms       float64 `json:"delivery_p50_ms"`
	DeliveryP99Ms       float64 `json:"delivery_p99_ms"`
	DeliveryMaxMs       float64 `json:"delivery_max_ms"`
	HealthyDisconnected int     `json:"healthy_disconnected"`
	StuckTotal          int     `json:"stuck_subscribers"`
	StuckDropped        int     `json:"stuck_dropped"`
	StuckBytesBuffered  int64   `json:"stuck_bytes_buffered"`
	ServerDropped       int64   `json:"server_dropped_subscribers"`
	PublishErrors       int64   `json:"publish_errors"`
	ConnectErrors       int     `json:"connect_errors"`
	FirstProblem        string  `json:"first_problem,omitempty"`
}

func main() {
	var c config
	flag.StringVar(&c.addr, "addr", "127.0.0.1:6383", "server address")
	flag.IntVar(&c.subs, "subs", 200, "healthy subscribers")
	flag.IntVar(&c.stuck, "stuck", 0, "subscribers that never read")
	flag.IntVar(&c.channels, "channels", 10, "channels; subscriber i listens on channel i mod channels")
	flag.IntVar(&c.publishers, "publishers", 4, "publisher connections")
	flag.IntVar(&c.size, "size", 256, "payload bytes")
	flag.IntVar(&c.rate, "rate", 500, "total PUBLISH per second, 0 for as fast as possible")
	flag.DurationVar(&c.duration, "duration", 15*time.Second, "publish time")
	flag.DurationVar(&c.drain, "drain", 2*time.Second, "wait for the last deliveries")
	flag.BoolVar(&c.pattern, "pattern", false, "subscribe with PSUBSCRIBE 'bench:<n>:*' instead of SUBSCRIBE")
	flag.Parse()
	if c.subs < 1 || c.channels < 1 || c.publishers < 1 || c.size < 24 || c.stuck < 0 {
		fmt.Fprintln(os.Stderr, "invalid flags: need subs>=1 channels>=1 publishers>=1 size>=24 stuck>=0")
		os.Exit(2)
	}
	res, err := run(c)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(res)
}

func channelName(c config, n int) string {
	if c.pattern {
		return fmt.Sprintf("bench:%d:x", n)
	}
	return fmt.Sprintf("bench:%d", n)
}

func encode(args ...string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	return []byte(b.String())
}

// readReply reads one reply. Arrays come back as their bulk elements.
func readReply(r *bufio.Reader) (kind byte, ints int64, items [][]byte, err error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return 0, 0, nil, err
	}
	if len(line) < 3 {
		return 0, 0, nil, errors.New("short reply")
	}
	kind = line[0]
	body := strings.TrimRight(line[1:], "\r\n")
	switch kind {
	case '+', '-':
		return kind, 0, [][]byte{[]byte(body)}, nil
	case ':':
		n, _ := strconv.ParseInt(body, 10, 64)
		return kind, n, nil, nil
	case '$':
		n, _ := strconv.Atoi(body)
		if n < 0 {
			return kind, 0, nil, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return 0, 0, nil, err
		}
		return kind, 0, [][]byte{buf[:n]}, nil
	case '*', '>':
		n, _ := strconv.Atoi(body)
		for i := 0; i < n; i++ {
			k, v, sub, err := readReply(r)
			if err != nil {
				return 0, 0, nil, err
			}
			switch k {
			case ':':
				items = append(items, []byte(strconv.FormatInt(v, 10)))
			default:
				if len(sub) > 0 {
					items = append(items, sub[0])
				} else {
					items = append(items, nil)
				}
			}
		}
		return '*', 0, items, nil
	}
	return kind, 0, nil, fmt.Errorf("unexpected reply %q", line)
}

type subscriber struct {
	conn      net.Conn
	reader    *bufio.Reader
	channel   int
	got       atomic.Int64
	lost      atomic.Bool
	latencies []int64 // sampled, nanoseconds
}

func dial(addr string) (net.Conn, error) {
	return net.DialTimeout("tcp", addr, 5*time.Second)
}

func subscribe(c config, id int, stuck bool) (*subscriber, error) {
	conn, err := dial(c.addr)
	if err != nil {
		return nil, err
	}
	if stuck {
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetReadBuffer(64 << 10)
		}
	}
	s := &subscriber{conn: conn, reader: bufio.NewReaderSize(conn, 16<<10), channel: id % c.channels}
	cmd := encode("SUBSCRIBE", channelName(c, s.channel))
	if c.pattern {
		cmd = encode("PSUBSCRIBE", fmt.Sprintf("bench:%d:*", s.channel))
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(cmd); err != nil {
		conn.Close()
		return nil, err
	}
	if _, _, _, err := readReply(s.reader); err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return s, nil
}

func (s *subscriber) read(stop <-chan struct{}) {
	count := 0
	for {
		_, _, items, err := readReply(s.reader)
		if err != nil {
			select {
			case <-stop:
			default:
				s.lost.Store(true)
			}
			return
		}
		if len(items) < 3 {
			continue
		}
		payload := items[len(items)-1]
		s.got.Add(1)
		count++
		if count%8 != 0 {
			continue
		}
		if i := strings.IndexByte(string(payload[:min(len(payload), 24)]), ':'); i > 0 {
			if ts, err := strconv.ParseInt(string(payload[:i]), 10, 64); err == nil {
				if d := time.Now().UnixNano() - ts; d >= 0 && len(s.latencies) < 100000 {
					s.latencies = append(s.latencies, d)
				}
			}
		}
	}
}

func percentile(sorted []int64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted)-1) * p)
	return float64(sorted[i]) / 1e6
}

func serverInfo(addr string) (name string, dropped int64) {
	dropped = -1
	conn, err := dial(addr)
	if err != nil {
		return "unknown", dropped
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(encode("INFO")); err != nil {
		return "unknown", dropped
	}
	_, _, items, err := readReply(bufio.NewReader(conn))
	if err != nil || len(items) == 0 {
		return "unknown", dropped
	}
	name = "redis-compatible"
	for _, line := range strings.Split(string(items[0]), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "redis_version:"):
			name = "redis " + strings.TrimPrefix(line, "redis_version:")
		case strings.HasPrefix(line, "pubsub_subscribers_dropped:"):
			dropped, _ = strconv.ParseInt(strings.TrimPrefix(line, "pubsub_subscribers_dropped:"), 10, 64)
		}
	}
	return name, dropped
}

func run(c config) (*result, error) {
	res := &result{
		Subscribers: c.subs, Channels: c.channels, Publishers: c.publishers, Pattern: c.pattern,
		PayloadBytes: c.size, TargetRate: c.rate, StuckTotal: c.stuck,
	}
	res.Server, _ = serverInfo(c.addr)
	_, droppedBefore := serverInfo(c.addr)

	var subs []*subscriber
	var stuck []*subscriber
	stop := make(chan struct{})
	var readers sync.WaitGroup
	sem := make(chan struct{}, 64)
	var mu sync.Mutex
	var wg sync.WaitGroup
	problem := func(msg string) {
		mu.Lock()
		if res.FirstProblem == "" {
			res.FirstProblem = msg
		}
		mu.Unlock()
	}
	for i := 0; i < c.subs+c.stuck; i++ {
		isStuck := i >= c.subs
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			s, err := subscribe(c, i, isStuck)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				res.ConnectErrors++
				if res.FirstProblem == "" {
					res.FirstProblem = "connect: " + err.Error()
				}
				return
			}
			if isStuck {
				stuck = append(stuck, s)
			} else {
				subs = append(subs, s)
			}
		}(i)
	}
	wg.Wait()
	if len(subs) == 0 {
		return nil, fmt.Errorf("no subscriber could connect: %s", res.FirstProblem)
	}
	perChannel := make([]int64, c.channels)
	for _, s := range subs {
		perChannel[s.channel]++
		readers.Add(1)
		go func(s *subscriber) { defer readers.Done(); s.read(stop) }(s)
	}

	published := make([]atomic.Int64, c.channels)
	var counter atomic.Int64
	var pubErrors atomic.Int64
	var pubLat [][]int64
	var latMu sync.Mutex
	payloadPad := strings.Repeat("x", c.size)
	start := time.Now()
	end := start.Add(c.duration)
	var pw sync.WaitGroup
	for p := 0; p < c.publishers; p++ {
		pw.Add(1)
		go func() {
			defer pw.Done()
			conn, err := dial(c.addr)
			if err != nil {
				pubErrors.Add(1)
				problem("publisher connect: " + err.Error())
				return
			}
			defer conn.Close()
			r := bufio.NewReader(conn)
			var lats []int64
			var interval time.Duration
			if c.rate > 0 {
				interval = time.Duration(float64(time.Second) * float64(c.publishers) / float64(c.rate))
			}
			next := time.Now()
			for time.Now().Before(end) {
				n := int(counter.Add(1) - 1)
				ch := n % c.channels
				stamp := strconv.FormatInt(time.Now().UnixNano(), 10) + ":"
				payload := (stamp + payloadPad)[:max(c.size, len(stamp))]
				t0 := time.Now()
				_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
				if _, err := conn.Write(encode("PUBLISH", channelName(c, ch), payload)); err != nil {
					pubErrors.Add(1)
					problem("publish: " + err.Error())
					return
				}
				kind, _, items, err := readReply(r)
				if err != nil || kind == '-' {
					pubErrors.Add(1)
					if err == nil && len(items) > 0 {
						err = errors.New(string(items[0]))
					}
					problem("publish: " + err.Error())
					return
				}
				lats = append(lats, time.Since(t0).Nanoseconds())
				published[ch].Add(1)
				if interval > 0 {
					next = next.Add(interval)
					if d := time.Until(next); d > 0 {
						time.Sleep(d)
					} else if d < -time.Second {
						next = time.Now()
					}
				}
			}
			latMu.Lock()
			pubLat = append(pubLat, lats)
			latMu.Unlock()
		}()
	}
	pw.Wait()
	elapsed := time.Since(start)
	time.Sleep(c.drain)
	close(stop)
	for _, s := range subs {
		_ = s.conn.SetReadDeadline(time.Now())
	}
	readers.Wait()

	var all []int64
	for _, l := range pubLat {
		all = append(all, l...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	res.Seconds = elapsed.Seconds()
	for ch := range published {
		n := published[ch].Load()
		res.Published += n
		res.Expected += n * perChannel[ch]
	}
	res.PublishPerSec = float64(res.Published) / res.Seconds
	res.PublishP50Ms, res.PublishP99Ms = percentile(all, 0.5), percentile(all, 0.99)
	if len(all) > 0 {
		res.PublishMaxMs = float64(all[len(all)-1]) / 1e6
	}
	res.PublishErrors = pubErrors.Load()

	var dl []int64
	for _, s := range subs {
		res.Delivered += s.got.Load()
		dl = append(dl, s.latencies...)
		if s.lost.Load() {
			res.HealthyDisconnected++
		}
		s.conn.Close()
	}
	sort.Slice(dl, func(i, j int) bool { return dl[i] < dl[j] })
	if res.Expected > 0 {
		res.DeliveredShare = float64(res.Delivered) / float64(res.Expected)
	}
	res.DeliveriesPerSec = float64(res.Delivered) / res.Seconds
	res.DeliveryP50Ms, res.DeliveryP99Ms = percentile(dl, 0.5), percentile(dl, 0.99)
	if len(dl) > 0 {
		res.DeliveryMaxMs = float64(dl[len(dl)-1]) / 1e6
	}

	// A stuck subscriber was dropped if the server closed its connection. Read
	// what the server had already buffered for it; a live connection goes quiet.
	for _, s := range stuck {
		deadline := time.Now().Add(20 * time.Second)
		buf := make([]byte, 64<<10)
		dropped := false
		for time.Now().Before(deadline) {
			_ = s.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			n, err := s.reader.Read(buf)
			res.StuckBytesBuffered += int64(n)
			if err == nil {
				continue
			}
			var ne net.Error
			dropped = !(errors.As(err, &ne) && ne.Timeout())
			break
		}
		if dropped {
			res.StuckDropped++
		}
		s.conn.Close()
	}
	if _, after := serverInfo(c.addr); after >= 0 && droppedBefore >= 0 {
		res.ServerDropped = after - droppedBefore
	} else {
		res.ServerDropped = -1
	}
	return res, nil
}
