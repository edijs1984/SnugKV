package rpccache

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeCacheServer answers every command with a nil bulk string and counts the
// connections it accepted.
func fakeCacheServer(t *testing.T, delay time.Duration) (addr string, accepted *atomic.Int64) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted = new(atomic.Int64)
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func() {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
					time.Sleep(delay)
					c.Write([]byte("$-1\r\n"))
				}
			}()
		}
	}()
	return l.Addr().String(), accepted
}

func TestRESPCacheNeverOpensMoreThanItsPoolSize(t *testing.T) {
	addr, accepted := fakeCacheServer(t, 2*time.Millisecond)
	c := NewRESPCache(addr, 4, time.Second)
	defer c.Close()
	var wg sync.WaitGroup
	var failures atomic.Int64
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := c.Get([]byte("k")); err != nil {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := accepted.Load(); n > 4 {
		t.Fatalf("opened %d connections with a pool of 4", n)
	}
	if failures.Load() != 0 {
		t.Fatalf("%d calls failed; they should have waited for a connection", failures.Load())
	}
}

func TestRESPCacheBusyIsReportedAndRecovers(t *testing.T) {
	addr, _ := fakeCacheServer(t, 0)
	c := NewRESPCache(addr, 1, 40*time.Millisecond)
	defer c.Close()
	held, err := c.acquire() // the only connection is in use
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, _, err := c.Get([]byte("k")); err != ErrBusy {
		t.Fatalf("expected ErrBusy, got %v", err)
	}
	if waited := time.Since(start); waited < 30*time.Millisecond {
		t.Fatalf("gave up after %v; it should wait for the cache timeout", waited)
	}
	c.release(held, true)
	if _, _, err := c.Get([]byte("k")); err != nil {
		t.Fatalf("after release: %v", err)
	}
}

func TestProxyTreatsBusyCacheAsAMissNotAnOutage(t *testing.T) {
	u := newUpstream(t, echo)
	p := newProxy(t, u, busyCache{})
	for i := 0; i < 3; i++ {
		post(p, `{"jsonrpc":"2.0","id":1,"method":"getSlot"}`)
	}
	if s := p.Stats(); s.CacheBusy == 0 || s.CacheErrors != 0 || u.calls.Load() != 3 {
		t.Fatalf("stats %+v, node calls %d: busy must not pause the cache", s, u.calls.Load())
	}
}

type busyCache struct{}

func (busyCache) Get([]byte) ([]byte, bool, error)        { return nil, false, ErrBusy }
func (busyCache) Set([]byte, []byte, time.Duration) error { return ErrBusy }
