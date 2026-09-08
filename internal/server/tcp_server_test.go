package server

import (
	"bufio"
	"errors"
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
