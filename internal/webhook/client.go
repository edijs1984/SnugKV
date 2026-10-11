package webhook

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

// value is one decoded reply.
type value struct {
	kind  byte // '+', ':', '$', '*'
	str   string
	n     int64
	isNil bool
	arr   []value
}

type serverError string

func (e serverError) Error() string { return string(e) }

// kvClient is a small Redis-protocol client over loopback. It exists so the
// webhook module goes through the same front door as every other client:
// journaling, ACLs, replication and stream semantics all apply unchanged.
type kvClient struct {
	addr, user, pass string
	timeout          time.Duration
	pool             chan *kvConn
}

type kvConn struct {
	c net.Conn
	r *bufio.Reader
}

func newKVClient(addr, user, pass string, size int, timeout time.Duration) *kvClient {
	return &kvClient{addr: addr, user: user, pass: pass, timeout: timeout, pool: make(chan *kvConn, size)}
}

func (c *kvClient) dial() (*kvConn, error) {
	nc, err := net.DialTimeout("tcp", c.addr, 3*time.Second)
	if err != nil {
		return nil, err
	}
	kc := &kvConn{c: nc, r: bufio.NewReaderSize(nc, 32<<10)}
	if c.pass != "" {
		args := []string{"AUTH", c.pass}
		if c.user != "" {
			args = []string{"AUTH", c.user, c.pass}
		}
		if _, err := kc.do(3*time.Second, args); err != nil {
			nc.Close()
			return nil, fmt.Errorf("auth: %w", err)
		}
	}
	return kc, nil
}

func (c *kvClient) close() {
	for {
		select {
		case kc := <-c.pool:
			kc.c.Close()
		default:
			return
		}
	}
}

// do runs one command on a pooled connection.
func (c *kvClient) do(args ...string) (value, error) { return c.doTimeout(c.timeout, args...) }

// doTimeout is do with its own deadline, for blocking reads.
func (c *kvClient) doTimeout(timeout time.Duration, args ...string) (value, error) {
	var kc *kvConn
	select {
	case kc = <-c.pool:
	default:
		var err error
		if kc, err = c.dial(); err != nil {
			return value{}, err
		}
	}
	v, err := kc.do(timeout, args)
	var se serverError
	if err != nil && !errors.As(err, &se) {
		kc.c.Close()
		return value{}, err
	}
	select {
	case c.pool <- kc:
	default:
		kc.c.Close()
	}
	return v, err
}

func (kc *kvConn) do(timeout time.Duration, args []string) (value, error) {
	buf := make([]byte, 0, 64)
	buf = append(buf, '*')
	buf = strconv.AppendInt(buf, int64(len(args)), 10)
	buf = append(buf, '\r', '\n')
	for _, a := range args {
		buf = append(buf, '$')
		buf = strconv.AppendInt(buf, int64(len(a)), 10)
		buf = append(buf, '\r', '\n')
		buf = append(buf, a...)
		buf = append(buf, '\r', '\n')
	}
	kc.c.SetDeadline(time.Now().Add(timeout))
	if _, err := kc.c.Write(buf); err != nil {
		return value{}, err
	}
	return readValue(kc.r, 0)
}

func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	if len(line) < 2 || line[len(line)-2] != '\r' {
		return "", errors.New("malformed reply")
	}
	return line[:len(line)-2], nil
}

func readValue(r *bufio.Reader, depth int) (value, error) {
	if depth > 6 {
		return value{}, errors.New("reply nested too deeply")
	}
	kind, err := r.ReadByte()
	if err != nil {
		return value{}, err
	}
	line, err := readLine(r)
	if err != nil {
		return value{}, err
	}
	switch kind {
	case '+':
		return value{kind: '+', str: line}, nil
	case '-':
		return value{}, serverError(line)
	case ':':
		n, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			return value{}, errors.New("invalid integer reply")
		}
		return value{kind: ':', n: n}, nil
	case '$':
		n, err := strconv.Atoi(line)
		if err != nil || n < -1 || n > 64<<20 {
			return value{}, errors.New("invalid bulk length")
		}
		if n == -1 {
			return value{kind: '$', isNil: true}, nil
		}
		b := make([]byte, n+2)
		if _, err := io.ReadFull(r, b); err != nil {
			return value{}, err
		}
		return value{kind: '$', str: string(b[:n])}, nil
	case '*':
		n, err := strconv.Atoi(line)
		if err != nil || n < -1 || n > 1<<20 {
			return value{}, errors.New("invalid array length")
		}
		if n == -1 {
			return value{kind: '*', isNil: true}, nil
		}
		v := value{kind: '*', arr: make([]value, 0, min(n, 1024))}
		var first error
		for i := 0; i < n; i++ {
			e, err := readValue(r, depth+1)
			if err != nil {
				var se serverError
				if errors.As(err, &se) {
					if first == nil {
						first = err
					}
					continue
				}
				return value{}, err
			}
			v.arr = append(v.arr, e)
		}
		if first != nil {
			return v, first
		}
		return v, nil
	}
	return value{}, fmt.Errorf("unknown reply type %q", kind)
}

// streamEntry is one stream message.
type streamEntry struct {
	id     string
	fields map[string]string
}

func parseEntries(v value) []streamEntry {
	var out []streamEntry
	for _, e := range v.arr {
		if len(e.arr) != 2 {
			continue
		}
		fs := map[string]string{}
		for i := 0; i+1 < len(e.arr[1].arr); i += 2 {
			fs[e.arr[1].arr[i].str] = e.arr[1].arr[i+1].str
		}
		out = append(out, streamEntry{id: e.arr[0].str, fields: fs})
	}
	return out
}

// guard serialises access to a value that several goroutines touch.
type guard[T any] struct {
	mu sync.RWMutex
	v  T
}

func (g *guard[T]) get() T {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.v
}

func (g *guard[T]) set(v T) {
	g.mu.Lock()
	g.v = v
	g.mu.Unlock()
}
