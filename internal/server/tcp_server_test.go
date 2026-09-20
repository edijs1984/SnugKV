package server

import (
	"snugkv/internal/optimizer"
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"snugkv/internal/config"
	"snugkv/internal/engine"
)

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}

func TestTCPServerHandlesPing(t *testing.T) {
	store := engine.New()
	server, err := Listen("127.0.0.1:0", store)
	if err != nil {
		t.Fatalf("Listen returned error: %v", err)
	}
	defer server.Close()

	addr := server.listener.Addr().String()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("Dial returned error: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("*1\r\n$4\r\nPING\r\n")); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	buf := make([]byte, 1024)
	if n, err := conn.Read(buf); err != nil {
		t.Fatalf("Read error: %v", err)
	} else if got, want := string(buf[:n]), "+PONG\r\n"; got != want {
		t.Fatalf("PING response mismatch: got %q want %q", got, want)
	}
}

func connectTestServer(t *testing.T) net.Conn {
	t.Helper()
	s, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	conn, err := net.DialTimeout("tcp", s.listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	return conn
}
func TestTCPBinaryPipeline(t *testing.T) {
	conn := connectTestServer(t)
	wire := "*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$5\r\na\r\n\x00b\r\n*2\r\n$3\r\nGET\r\n$1\r\nk\r\n*1\r\n$4\r\nPING\r\n"
	for i := range wire {
		if _, err := conn.Write([]byte{wire[i]}); err != nil {
			t.Fatal(err)
		}
	}
	want := "+OK\r\n$5\r\na\r\n\x00b\r\n+PONG\r\n"
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("got %q", got)
	}
}
func TestTCPMalformedCloses(t *testing.T) {
	for _, wire := range []string{"*1\r\n+PING\r\n", "*999999999999\r\n", "*1\r\n$33554433\r\n"} {
		conn := connectTestServer(t)
		if _, err := io.WriteString(conn, wire); err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(conn)
		if err != nil || string(got) != "-ERR invalid RESP\r\n" {
			t.Fatalf("got %q %v", got, err)
		}
	}
}
func TestTCPErrorFramingAndRecovery(t *testing.T) {
	conn := connectTestServer(t)
	wire := "*1\r\n$3\r\nGET\r\n*1\r\n$4\r\nPING\r\n"
	io.WriteString(conn, wire)
	want := "-ERR wrong number of arguments for 'get' command\r\n+PONG\r\n"
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("got %q", got)
	}
	if got := string(errorResponse(errors.New("ERR bad\r\n+injected"))); got != "-ERR bad  +injected\r\n" {
		t.Fatalf("unsafe error %q", got)
	}
}

type shortWriteConn struct {
	net.Conn
	zero bool
}

func (c shortWriteConn) Write(p []byte) (int, error) {
	if c.zero {
		return 0, nil
	}
	if len(p) > 2 {
		p = p[:2]
	}
	return c.Conn.Write(p)
}
func TestResponseShortWrites(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	right.SetReadDeadline(time.Now().Add(time.Second))
	done := make(chan error, 1)
	go func() { done <- writeResponse(shortWriteConn{Conn: left}, []byte("+PONG\r\n")) }()
	got := make([]byte, 7)
	if _, err := io.ReadFull(right, got); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil || string(got) != "+PONG\r\n" {
		t.Fatalf("%q %v", got, err)
	}
	if err := writeResponse(shortWriteConn{Conn: left, zero: true}, []byte("x")); err != io.ErrShortWrite {
		t.Fatalf("zero write: %v", err)
	}
}

func TestShutdownAndQuit(t *testing.T) {
	s, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", s.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(time.Second))
	io.WriteString(c, "*1\r\n$4\r\nQUIT\r\n")
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "+OK\r\n" {
		t.Fatalf("quit %q %v", got, err)
	}
	c2, err := net.Dial("tcp", s.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	c2.SetDeadline(time.Now().Add(time.Second))
	io.WriteString(c2, "*1\r\n$")
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = c2.Read(make([]byte, 1)); err == nil {
		t.Fatal("client remained open")
	}
}

func TestConnectionLimitAndDeadline(t *testing.T) {
	cfg := config.Default()
	cfg.ListenAddr = "127.0.0.1:0"
	cfg.MaxConnections = 1
	cfg.ReadTimeoutMS = 40
	s, err := ListenWithConfig(cfg, engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first, err := net.Dial("tcp", s.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	first.SetDeadline(time.Now().Add(time.Second))
	io.WriteString(first, "*1\r\n$4\r\nPING\r\n")
	reply := make([]byte, 7)
	if _, err = io.ReadFull(first, reply); err != nil {
		t.Fatal(err)
	}
	second, err := net.Dial("tcp", s.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	second.SetDeadline(time.Now().Add(time.Second))
	if _, err = second.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection limit ignored")
	}
	if _, err = io.ReadAll(first); err != nil {
		t.Fatalf("read deadline did not close client: %v", err)
	}
}

func TestSeparateAdminListener(t *testing.T) {
	cfg := config.Default()
	cfg.ListenAddr = "127.0.0.1:0"
	cfg.AdminAddr = freeAddress(t)
	s, err := ListenWithConfig(cfg, engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.OpenAdmin(cfg.AdminAddr); err != nil {
		t.Fatal(err)
	}
	public, err := net.Dial("tcp", s.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer public.Close()
	public.SetDeadline(time.Now().Add(time.Second))
	io.WriteString(public, "*1\r\n$10\r\nSNUG.STATS\r\n")
	line, _ := bufio.NewReader(public).ReadString('\n')
	if line != "-ERR use the admin listener\r\n" {
		t.Fatalf("public admin response %q", line)
	}
	admin, err := net.Dial("tcp", cfg.AdminAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	admin.SetDeadline(time.Now().Add(time.Second))
	io.WriteString(admin, "*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n")
	line, _ = bufio.NewReader(admin).ReadString('\n')
	if line != "-ERR command is unavailable on admin listener\r\n" {
		t.Fatalf("admin mutation response %q", line)
	}
}

func TestMetricsListener(t *testing.T) {
	s, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	address := freeAddress(t)
	metrics, err := s.Metrics(address)
	if err != nil {
		t.Fatal(err)
	}
	defer metrics.Close()
	response, err := http.Get("http://" + address + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "snugkv_memory_accounted_bytes") {
		t.Fatalf("missing engine metrics: %s", body)
	}
	if _, err = s.Metrics("0.0.0.0:0"); err == nil {
		t.Fatal("metrics accepted a non-loopback address")
	}
}

func TestConnectionPanicRecovery(t *testing.T) {
	done := make(chan struct{})

	go func() {
		defer close(done)
		defer recoverConnectionPanic()

		panic("test connection panic")
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("connection panic was not recovered")
	}
}

func TestTCPBulkSizeBoundary(t *testing.T) {
	const maxBulk = 32 << 20

	store := engine.New()
	s, err := Listen("127.0.0.1:0", store)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	addr := s.listener.Addr().String()

	t.Run("accepts exact maximum bulk", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()

		// Large local transfers can take longer under -race.
		if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
			t.Fatal(err)
		}

		header := fmt.Sprintf(
			"*3\r\n$3\r\nSET\r\n$8\r\nboundary\r\n$%d\r\n",
			maxBulk,
		)

		if _, err := io.WriteString(conn, header); err != nil {
			t.Fatal(err)
		}

		chunk := make([]byte, 64<<10)
		for i := range chunk {
			chunk[i] = 'x'
		}

		remaining := maxBulk
		for remaining > 0 {
			n := len(chunk)
			if n > remaining {
				n = remaining
			}

			if _, err := conn.Write(chunk[:n]); err != nil {
				t.Fatal(err)
			}

			remaining -= n
		}

		if _, err := io.WriteString(conn, "\r\n"); err != nil {
			t.Fatal(err)
		}

		reader := bufio.NewReader(conn)

		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line != "+OK\r\n" {
			t.Fatalf("SET response = %q", line)
		}

		if _, err := io.WriteString(
			conn,
			"*2\r\n$6\r\nSTRLEN\r\n$8\r\nboundary\r\n",
		); err != nil {
			t.Fatal(err)
		}

		line, err = reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}

		want := fmt.Sprintf(":%d\r\n", maxBulk)
		if line != want {
			t.Fatalf("STRLEN response = %q, want %q", line, want)
		}
	})

	t.Run("rejects one byte above maximum", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()

		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}

		// The decoder must reject the declared bulk length before attempting
		// to read or allocate the payload.
		wire := fmt.Sprintf(
			"*3\r\n$3\r\nSET\r\n$9\r\ntoo-large\r\n$%d\r\n",
			maxBulk+1,
		)

		if _, err := io.WriteString(conn, wire); err != nil {
			t.Fatal(err)
		}

		got, err := io.ReadAll(conn)
		if err != nil {
			t.Fatal(err)
		}

		if string(got) != "-ERR invalid RESP\r\n" {
			t.Fatalf("oversized response = %q", got)
		}
	})

	t.Run("server remains alive after rejection", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()

		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}

		if _, err := io.WriteString(
			conn,
			"*1\r\n$4\r\nPING\r\n",
		); err != nil {
			t.Fatal(err)
		}

		reply := make([]byte, len("+PONG\r\n"))
		if _, err := io.ReadFull(conn, reply); err != nil {
			t.Fatal(err)
		}

		if string(reply) != "+PONG\r\n" {
			t.Fatalf("PING response = %q", reply)
		}
	})
}


func TestOptimizeSampleDoesNotContinuouslyRescanWithoutDrops(t *testing.T) {
	store, err := engine.NewWithOptions(engine.Options{
		Shards:      1,
		Encoding:    true,
		Compression: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	s := New(store)
	opt, err := optimizer.New(store, optimizer.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer opt.Close()
	s.optimizer = opt

	tcp := &TCPServer{server: s}

	for i := 0; i < 99; i++ {
		tcp.OptimizeSample()
	}

	if got := opt.Stats().Queued; got != 0 {
		t.Fatalf("queued=%d before periodic discovery, want 0", got)
	}
}
