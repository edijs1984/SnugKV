package rpccache

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// Cache stores response bodies. Implementations must be safe for concurrent
// use. Errors are treated as misses by the proxy.
type Cache interface {
	Get(key []byte) (value []byte, found bool, err error)
	Set(key, value []byte, ttl time.Duration) error
}

// RESPCache talks to any Redis-protocol server (SnugKV or Redis) over a small
// connection pool.
type RESPCache struct {
	addr    string
	timeout time.Duration
	pool    chan *respConn
}

type respConn struct {
	c net.Conn
	r *bufio.Reader
	w []byte
}

// NewRESPCache returns a cache using up to size connections. Connections are
// opened lazily, so a missing server does not fail construction.
func NewRESPCache(addr string, size int, timeout time.Duration) *RESPCache {
	if size < 1 {
		size = 8
	}
	if timeout <= 0 {
		timeout = 100 * time.Millisecond
	}
	return &RESPCache{addr: addr, timeout: timeout, pool: make(chan *respConn, size)}
}

func (c *RESPCache) acquire() (*respConn, error) {
	select {
	case rc := <-c.pool:
		return rc, nil
	default:
	}
	conn, err := net.DialTimeout("tcp", c.addr, c.timeout)
	if err != nil {
		return nil, err
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	return &respConn{c: conn, r: bufio.NewReaderSize(conn, 32<<10)}, nil
}

func (c *RESPCache) release(rc *respConn, healthy bool) {
	if !healthy {
		rc.c.Close()
		return
	}
	select {
	case c.pool <- rc:
	default:
		rc.c.Close()
	}
}

// Close drops idle connections.
func (c *RESPCache) Close() {
	for {
		select {
		case rc := <-c.pool:
			rc.c.Close()
		default:
			return
		}
	}
}

func appendCommand(dst []byte, args ...[]byte) []byte {
	dst = append(dst, '*')
	dst = strconv.AppendInt(dst, int64(len(args)), 10)
	dst = append(dst, '\r', '\n')
	for _, a := range args {
		dst = append(dst, '$')
		dst = strconv.AppendInt(dst, int64(len(a)), 10)
		dst = append(dst, '\r', '\n')
		dst = append(dst, a...)
		dst = append(dst, '\r', '\n')
	}
	return dst
}

func (c *RESPCache) roundTrip(rc *respConn, req []byte) (reply, error) {
	rc.c.SetDeadline(time.Now().Add(c.timeout))
	if _, err := rc.c.Write(req); err != nil {
		return reply{}, err
	}
	return readReply(rc.r)
}

func (c *RESPCache) Get(key []byte) ([]byte, bool, error) {
	rc, err := c.acquire()
	if err != nil {
		return nil, false, err
	}
	rc.w = appendCommand(rc.w[:0], []byte("GET"), key)
	rep, err := c.roundTrip(rc, rc.w)
	if err != nil {
		c.release(rc, false)
		return nil, false, err
	}
	c.release(rc, rep.kind != '-')
	switch rep.kind {
	case '$':
		return rep.data, !rep.nil, nil
	case '-':
		return nil, false, errors.New(string(rep.data))
	}
	return nil, false, fmt.Errorf("unexpected reply %q to GET", rep.kind)
}

func (c *RESPCache) Set(key, value []byte, ttl time.Duration) error {
	rc, err := c.acquire()
	if err != nil {
		return err
	}
	ms := ttl.Milliseconds()
	if ms < 1 {
		ms = 1
	}
	rc.w = appendCommand(rc.w[:0], []byte("SET"), key, value, []byte("PX"), strconv.AppendInt(nil, ms, 10))
	rep, err := c.roundTrip(rc, rc.w)
	if err != nil {
		c.release(rc, false)
		return err
	}
	c.release(rc, rep.kind != '-')
	switch rep.kind {
	case '+':
		return nil
	case '-':
		return errors.New(string(rep.data))
	}
	return fmt.Errorf("unexpected reply %q to SET", rep.kind)
}

type reply struct {
	kind byte
	data []byte
	nil  bool
}

func readReply(r *bufio.Reader) (reply, error) {
	kind, err := r.ReadByte()
	if err != nil {
		return reply{}, err
	}
	line, err := readLine(r)
	if err != nil {
		return reply{}, err
	}
	switch kind {
	case '+', '-', ':':
		return reply{kind: kind, data: line}, nil
	case '$':
		n, err := strconv.Atoi(string(line))
		if err != nil || n < -1 || n > 512<<20 {
			return reply{}, errors.New("invalid bulk length")
		}
		if n == -1 {
			return reply{kind: '$', nil: true}, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return reply{}, err
		}
		if buf[n] != '\r' || buf[n+1] != '\n' {
			return reply{}, errors.New("malformed bulk reply")
		}
		return reply{kind: '$', data: buf[:n]}, nil
	}
	return reply{}, fmt.Errorf("unsupported reply type %q", kind)
}

func readLine(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	if len(line) < 2 || line[len(line)-2] != '\r' {
		return nil, errors.New("malformed reply line")
	}
	return line[:len(line)-2], nil
}

// MemoryCache is an in-process Cache for tests and single-binary demos.
type MemoryCache struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]memEntry
}

type memEntry struct {
	v   []byte
	exp time.Time
}

func NewMemoryCache() *MemoryCache {
	return &MemoryCache{now: time.Now, m: make(map[string]memEntry)}
}

func (c *MemoryCache) Get(key []byte) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[string(key)]
	if !ok || !c.now().Before(e.exp) {
		delete(c.m, string(key))
		return nil, false, nil
	}
	return append([]byte(nil), e.v...), true, nil
}

func (c *MemoryCache) Set(key, value []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[string(key)] = memEntry{v: append([]byte(nil), value...), exp: c.now().Add(ttl)}
	return nil
}

func (c *MemoryCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}
