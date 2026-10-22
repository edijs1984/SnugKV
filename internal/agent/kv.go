package agent

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// kvConn is one connection to SnugKV (redis://, rediss://, snugkv://,
// snugkvs://, with user:password), reconnected on the next call after an error.
type kvConn struct {
	addr, user, pass, serverName string
	tls                          *tls.Config
	c                            net.Conn
	r                            *bufio.Reader
	blockFor                     time.Duration // longest XREAD BLOCK on this connection
}

func parseKVURL(raw, caFile string) (*kvConn, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	k := &kvConn{addr: u.Host}
	switch u.Scheme {
	case "redis", "snugkv":
	case "rediss", "snugkvs":
		tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}
		if caFile != "" {
			pem, err := os.ReadFile(caFile)
			if err != nil {
				return nil, err
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, errors.New("the CA file holds no certificate")
			}
			tc.RootCAs = pool
		}
		k.tls = tc
	default:
		return nil, fmt.Errorf("unsupported URL scheme %q (redis://, rediss://)", u.Scheme)
	}
	if u.Port() == "" {
		k.addr = net.JoinHostPort(u.Hostname(), "6379")
	}
	if u.User != nil {
		k.user = u.User.Username()
		k.pass, _ = u.User.Password()
		if k.pass == "" { // redis://password@host
			k.pass, k.user = k.user, ""
		}
	}
	return k, nil
}

func (k *kvConn) close() {
	if k.c != nil {
		k.c.Close()
		k.c = nil
	}
}

func (k *kvConn) dial() error {
	d := net.Dialer{Timeout: 5 * time.Second}
	var c net.Conn
	var err error
	if k.tls != nil {
		c, err = tls.DialWithDialer(&d, "tcp", k.addr, k.tls)
	} else {
		c, err = d.Dial("tcp", k.addr)
	}
	if err != nil {
		return err
	}
	k.c, k.r = c, bufio.NewReader(c)
	if k.pass != "" {
		args := []string{"AUTH", k.pass}
		if k.user != "" {
			args = []string{"AUTH", k.user, k.pass}
		}
		if _, err := k.do(args...); err != nil {
			k.close()
			return fmt.Errorf("auth: %w", err)
		}
	}
	return nil
}

// do sends one command and returns a simple reply (string, int or nil).
func (k *kvConn) do(args ...string) (string, error) {
	v, _, err := k.send(args, false)
	return v, err
}

// doList sends one command whose reply is a flat array (HGETALL, ...).
func (k *kvConn) doList(args ...string) ([]string, error) {
	_, list, err := k.send(args, true)
	return list, err
}

// doAny sends one command and returns the reply as nested values: string,
// nil, or []any (XREAD and the like).
func (k *kvConn) doAny(args ...string) (any, error) {
	if err := k.write(args); err != nil {
		return nil, err
	}
	v, err := readValue(k.r)
	if err != nil {
		var se serverErr
		if !errors.As(err, &se) {
			k.close()
		}
	}
	return v, err
}

func (k *kvConn) write(args []string) error {
	if k.c == nil {
		if err := k.dial(); err != nil {
			return err
		}
	}
	var b strings.Builder
	b.WriteString("*" + strconv.Itoa(len(args)) + "\r\n")
	for _, a := range args {
		b.WriteString("$" + strconv.Itoa(len(a)) + "\r\n" + a + "\r\n")
	}
	k.c.SetDeadline(time.Now().Add(k.timeout()))
	if _, err := io.WriteString(k.c, b.String()); err != nil {
		k.close()
		return err
	}
	return nil
}

// timeout of one call; blocking reads (XREAD BLOCK) get longer.
func (k *kvConn) timeout() time.Duration {
	if k.blockFor > 0 {
		return k.blockFor + 5*time.Second
	}
	return 5 * time.Second
}

// readValue reads any reply, arrays (and RESP3 maps) as []any.
func readValue(r *bufio.Reader) (any, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return nil, errors.New("empty reply")
	}
	switch line[0] {
	case '+', ':':
		return line[1:], nil
	case '-':
		return nil, serverErr(line[1:])
	case '_':
		return nil, nil
	case '$':
		n, _ := strconv.Atoi(line[1:])
		if n < 0 {
			return nil, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return string(buf[:n]), nil
	case '*', '%':
		n, _ := strconv.Atoi(line[1:])
		if n < 0 {
			return nil, nil
		}
		if line[0] == '%' {
			n *= 2
		}
		out := make([]any, 0, n)
		for i := 0; i < n; i++ {
			v, err := readValue(r)
			if err != nil {
				var se serverErr
				if !errors.As(err, &se) {
					return nil, err
				}
			}
			out = append(out, v)
		}
		return out, nil
	}
	return nil, fmt.Errorf("unexpected reply %q", line)
}

func (k *kvConn) send(args []string, wantList bool) (string, []string, error) {
	if k.c == nil {
		if err := k.dial(); err != nil {
			return "", nil, err
		}
	}
	var b strings.Builder
	b.WriteString("*" + strconv.Itoa(len(args)) + "\r\n")
	for _, a := range args {
		b.WriteString("$" + strconv.Itoa(len(a)) + "\r\n" + a + "\r\n")
	}
	k.c.SetDeadline(time.Now().Add(k.timeout()))
	if _, err := io.WriteString(k.c, b.String()); err != nil {
		k.close()
		return "", nil, err
	}
	var (
		v    string
		list []string
		err  error
	)
	if wantList {
		list, err = readList(k.r)
	} else {
		v, err = readReply(k.r)
	}
	if err != nil {
		var se serverErr
		if !errors.As(err, &se) {
			k.close()
		}
	}
	return v, list, err
}

// readList reads an array of simple values (nil array: empty).
func readList(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return nil, errors.New("empty reply")
	}
	switch line[0] {
	case '-':
		return nil, serverErr(line[1:])
	case '*', '%':
		n, _ := strconv.Atoi(line[1:])
		if line[0] == '%' {
			n *= 2
		}
		out := make([]string, 0, max(n, 0))
		for i := 0; i < n; i++ {
			v, err := readReply(r)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	}
	return nil, fmt.Errorf("expected an array, got %q", line)
}

type serverErr string

func (e serverErr) Error() string { return string(e) }

// readReply reads one reply; arrays are read and dropped (the agent never needs them).
func readReply(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errors.New("empty reply")
	}
	switch line[0] {
	case '+', ':':
		return line[1:], nil
	case '-':
		return "", serverErr(line[1:])
	case '$':
		n, _ := strconv.Atoi(line[1:])
		if n < 0 {
			return "", nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		return string(buf[:n]), nil
	case '*':
		n, _ := strconv.Atoi(line[1:])
		for i := 0; i < n; i++ {
			if _, err := readReply(r); err != nil {
				var se serverErr
				if !errors.As(err, &se) {
					return "", err
				}
			}
		}
		return "", nil
	}
	return "", fmt.Errorf("unexpected reply %q", line)
}
