package rpccache

import (
	"bytes"
	"net"
	"testing"
	"time"

	"snugkv/internal/engine"
	"snugkv/internal/server"
)

// startSnug runs a real SnugKV server in-process.
func startSnug(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	store, err := engine.NewWithOptions(engine.Options{Shards: 4, Encoding: true, Compression: true, ShapeEncoding: true})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := server.Listen(addr, store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return addr
}

func TestRESPCacheAgainstSnugKV(t *testing.T) {
	c := NewRESPCache(startSnug(t), 4, time.Second)
	defer c.Close()

	key := []byte("r\x00\x01\xff binary key")
	if _, ok, err := c.Get(key); err != nil || ok {
		t.Fatalf("missing key: ok=%v err=%v", ok, err)
	}
	val := bytes.Repeat([]byte(`{"a":"b"}`), 5000) // larger than a read buffer
	if err := c.Set(key, val, 200*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.Get(key)
	if err != nil || !ok || !bytes.Equal(got, val) {
		t.Fatalf("round trip: ok=%v err=%v len=%d", ok, err, len(got))
	}
	time.Sleep(300 * time.Millisecond)
	if _, ok, _ := c.Get(key); ok {
		t.Fatal("entry did not expire")
	}
	// An empty value is a value, not a miss.
	c.Set([]byte("empty"), nil, time.Second)
	if v, ok, err := c.Get([]byte("empty")); err != nil || !ok || len(v) != 0 {
		t.Fatalf("empty value: %v %v %v", v, ok, err)
	}
}

func TestRESPCacheReconnectsAndFailsFast(t *testing.T) {
	c := NewRESPCache("127.0.0.1:1", 2, 50*time.Millisecond)
	if _, _, err := c.Get([]byte("k")); err == nil {
		t.Fatal("expected a connection error")
	}
	if err := c.Set([]byte("k"), []byte("v"), time.Second); err == nil {
		t.Fatal("expected a connection error")
	}
}

func TestProxyEndToEndOnSnugKV(t *testing.T) {
	u := newUpstream(t, echo)
	c := NewRESPCache(startSnug(t), 8, time.Second)
	defer c.Close()
	p := newProxy(t, u, c)
	req := `{"jsonrpc":"2.0","id":1,"method":"getAccountInfo","params":["Tokenkeg"]}`
	post(p, req)
	rec, body := post(p, req)
	if rec.Header().Get("X-Cache") != "HIT" || u.calls.Load() != 1 {
		t.Fatalf("expected a hit from SnugKV: %s calls=%d body=%s", rec.Header().Get("X-Cache"), u.calls.Load(), body)
	}
}
