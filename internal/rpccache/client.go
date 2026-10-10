package rpccache

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// Value is one decoded Redis-protocol reply.
type Value struct {
	Kind  byte // '+' simple string, ':' integer, '$' bulk string, '*' array
	Str   string
	Int   int64
	Nil   bool
	Array []Value
}

// ServerError is an error reply from the server.
type ServerError string

func (e ServerError) Error() string { return string(e) }

// Client is a small pooled Redis-protocol client for commands the cache path
// does not use (API key records, rate limits, usage counters). It is safe for
// concurrent use. Connections open lazily.
type Client struct {
	addr    string
	timeout time.Duration
	pool    chan *clientConn
}

type clientConn struct {
	c net.Conn
	r *bufio.Reader
}

// NewClient returns a client with up to size pooled connections.
func NewClient(addr string, size int, timeout time.Duration) *Client {
	if size < 1 {
		size = 4
	}
	if timeout <= 0 {
		timeout = 250 * time.Millisecond
	}
	return &Client{addr: addr, timeout: timeout, pool: make(chan *clientConn, size)}
}

// Close drops idle connections.
func (c *Client) Close() {
	for {
		select {
		case cc := <-c.pool:
			cc.c.Close()
		default:
			return
		}
	}
}

func (c *Client) acquire() (*clientConn, error) {
	select {
	case cc := <-c.pool:
		return cc, nil
	default:
	}
	conn, err := net.DialTimeout("tcp", c.addr, c.timeout)
	if err != nil {
		return nil, err
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	return &clientConn{c: conn, r: bufio.NewReaderSize(conn, 16<<10)}, nil
}

func (c *Client) release(cc *clientConn, healthy bool) {
	if !healthy {
		cc.c.Close()
		return
	}
	select {
	case c.pool <- cc:
	default:
		cc.c.Close()
	}
}

// Do runs one command. A server error reply is returned as a ServerError.
func (c *Client) Do(args ...string) (Value, error) {
	vs, err := c.Pipeline([][]string{args})
	if err != nil {
		return Value{}, err
	}
	return vs[0], nil
}

// Pipeline sends every command before reading any reply. Replies that are
// server errors come back as ServerError for the first one encountered, after
// all replies were read, so the connection stays usable.
func (c *Client) Pipeline(cmds [][]string) ([]Value, error) {
	cc, err := c.acquire()
	if err != nil {
		return nil, err
	}
	var buf []byte
	for _, cmd := range cmds {
		buf = append(buf, '*')
		buf = strconv.AppendInt(buf, int64(len(cmd)), 10)
		buf = append(buf, '\r', '\n')
		for _, a := range cmd {
			buf = append(buf, '$')
			buf = strconv.AppendInt(buf, int64(len(a)), 10)
			buf = append(buf, '\r', '\n')
			buf = append(buf, a...)
			buf = append(buf, '\r', '\n')
		}
	}
	cc.c.SetDeadline(time.Now().Add(c.timeout))
	if _, err := cc.c.Write(buf); err != nil {
		c.release(cc, false)
		return nil, err
	}
	out := make([]Value, len(cmds))
	var first error
	for i := range cmds {
		v, err := readValue(cc.r, 0)
		if err != nil {
			var se ServerError
			if errors.As(err, &se) {
				if first == nil {
					first = se
				}
				continue
			}
			c.release(cc, false)
			return nil, err
		}
		out[i] = v
	}
	c.release(cc, true)
	if first != nil {
		return out, first
	}
	return out, nil
}

func readValue(r *bufio.Reader, depth int) (Value, error) {
	if depth > 4 {
		return Value{}, errors.New("reply nested too deeply")
	}
	kind, err := r.ReadByte()
	if err != nil {
		return Value{}, err
	}
	line, err := readLine(r)
	if err != nil {
		return Value{}, err
	}
	switch kind {
	case '+':
		return Value{Kind: '+', Str: string(line)}, nil
	case '-':
		return Value{}, ServerError(line)
	case ':':
		n, err := strconv.ParseInt(string(line), 10, 64)
		if err != nil {
			return Value{}, errors.New("invalid integer reply")
		}
		return Value{Kind: ':', Int: n}, nil
	case '$':
		n, err := strconv.Atoi(string(line))
		if err != nil || n < -1 || n > 64<<20 {
			return Value{}, errors.New("invalid bulk length")
		}
		if n == -1 {
			return Value{Kind: '$', Nil: true}, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return Value{}, err
		}
		return Value{Kind: '$', Str: string(buf[:n])}, nil
	case '*':
		n, err := strconv.Atoi(string(line))
		if err != nil || n < -1 || n > 1<<20 {
			return Value{}, errors.New("invalid array length")
		}
		if n == -1 {
			return Value{Kind: '*', Nil: true}, nil
		}
		v := Value{Kind: '*', Array: make([]Value, 0, min(n, 1024))}
		var first error
		for i := 0; i < n; i++ {
			e, err := readValue(r, depth+1)
			if err != nil {
				var se ServerError
				if errors.As(err, &se) {
					if first == nil {
						first = se
					}
					continue
				}
				return Value{}, err
			}
			v.Array = append(v.Array, e)
		}
		if first != nil {
			return Value{}, first
		}
		return v, nil
	}
	return Value{}, fmt.Errorf("unsupported reply type %q", kind)
}

// AsInt reads an integer from an integer or bulk-string reply.
func (v Value) AsInt() (int64, error) {
	switch v.Kind {
	case ':':
		return v.Int, nil
	case '$':
		if v.Nil {
			return 0, nil
		}
		return strconv.ParseInt(v.Str, 10, 64)
	}
	return 0, fmt.Errorf("reply %q is not a number", v.Kind)
}
