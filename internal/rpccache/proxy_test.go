package rpccache

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// upstream is a fake RPC node.
type upstream struct {
	calls   atomic.Int64
	handler func(method string, params json.RawMessage) (result string, rpcErr string)
	gate    chan struct{} // when set, requests block until it is closed
	status  int
	srv     *httptest.Server
}

func newUpstream(t testing.TB, h func(string, json.RawMessage) (string, string)) *upstream {
	u := &upstream{handler: h, status: 200}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.calls.Add(1)
		if u.gate != nil {
			<-u.gate
		}
		body, _ := io.ReadAll(r.Body)
		var req request
		json.Unmarshal(body, &req)
		if u.status != 200 {
			http.Error(w, "rate limited", u.status)
			return
		}
		result, rpcErr := u.handler(req.Method, req.Params)
		if rpcErr != "" {
			w.Write([]byte(`{"jsonrpc":"2.0","error":` + rpcErr + `,"id":` + string(req.ID) + `}`))
			return
		}
		w.Write([]byte(`{"jsonrpc":"2.0","result":` + result + `,"id":` + string(req.ID) + `}`))
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func echo(method string, params json.RawMessage) (string, string) {
	return `{"method":"` + method + `","params":` + string(params) + `}`, ""
}

func newProxy(t *testing.T, u *upstream, cache Cache) *Proxy {
	t.Helper()
	p, err := New(Config{Chain: Solana, Upstream: u.srv.URL, Cache: cache})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func post(p *Proxy, body string) (*httptest.ResponseRecorder, string) {
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	return rec, rec.Body.String()
}

func TestHitMissAndIDSubstitution(t *testing.T) {
	u := newUpstream(t, echo)
	p := newProxy(t, u, NewMemoryCache())

	rec, body := post(p, `{"jsonrpc":"2.0","id":7,"method":"getAccountInfo","params":["abc",{"encoding":"base64"}]}`)
	if rec.Header().Get("X-Cache") != "MISS" || !strings.Contains(body, `"id":7`) {
		t.Fatalf("first: %s %q", rec.Header().Get("X-Cache"), body)
	}
	rec, body2 := post(p, `{"jsonrpc":"2.0","id":"req-2","method":"getAccountInfo","params":["abc", {"encoding":"base64"}]}`)
	if rec.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("second request was %s", rec.Header().Get("X-Cache"))
	}
	if !strings.Contains(body2, `"id":"req-2"`) || strings.Contains(body2, `"id":7`) {
		t.Fatalf("id not substituted: %s", body2)
	}
	var a, b struct{ Result json.RawMessage }
	json.Unmarshal([]byte(body), &a)
	json.Unmarshal([]byte(body2), &b)
	var ca, cb bytes.Buffer
	json.Compact(&ca, a.Result)
	json.Compact(&cb, b.Result)
	if ca.String() != cb.String() {
		t.Fatalf("results differ:\n%s\n%s", ca.String(), cb.String())
	}
	if u.calls.Load() != 1 {
		t.Fatalf("upstream called %d times", u.calls.Load())
	}
	// A different argument is a different entry.
	post(p, `{"jsonrpc":"2.0","id":1,"method":"getAccountInfo","params":["other"]}`)
	if u.calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2", u.calls.Load())
	}
}

func TestNeverCachesErrorsOrNull(t *testing.T) {
	u := newUpstream(t, func(m string, _ json.RawMessage) (string, string) {
		if m == "getBalance" {
			return "", `{"code":-32602,"message":"bad"}`
		}
		return "null", ""
	})
	p := newProxy(t, u, NewMemoryCache())
	for i := 0; i < 3; i++ {
		_, body := post(p, `{"jsonrpc":"2.0","id":1,"method":"getBalance","params":["x"]}`)
		if !strings.Contains(body, `"error"`) || !strings.Contains(body, `"id":1`) {
			t.Fatalf("error not relayed: %s", body)
		}
		_, body = post(p, `{"jsonrpc":"2.0","id":2,"method":"getTransaction","params":["sig"]}`)
		if !strings.Contains(body, `"result":null`) {
			t.Fatalf("null not relayed: %s", body)
		}
	}
	if u.calls.Load() != 6 {
		t.Fatalf("upstream calls = %d, want 6 (nothing cached)", u.calls.Load())
	}
}

func TestBypassMethodsAreForwardedUntouched(t *testing.T) {
	u := newUpstream(t, echo)
	p := newProxy(t, u, NewMemoryCache())
	for i := 0; i < 3; i++ {
		rec, _ := post(p, `{"jsonrpc":"2.0","id":1,"method":"sendTransaction","params":["tx"]}`)
		if rec.Header().Get("X-Cache") != "BYPASS" {
			t.Fatalf("sendTransaction: %s", rec.Header().Get("X-Cache"))
		}
	}
	rec, _ := post(p, `{"jsonrpc":"2.0","id":1,"method":"getAccountInfo","params":["a",{"minContextSlot":9}]}`)
	if rec.Header().Get("X-Cache") != "BYPASS" {
		t.Fatalf("minContextSlot: %s", rec.Header().Get("X-Cache"))
	}
	if u.calls.Load() != 4 {
		t.Fatalf("calls = %d", u.calls.Load())
	}
	// Garbage is relayed, not rejected locally.
	_, body := post(p, `not json`)
	if body == "" {
		t.Fatal("garbage request got no relayed reply")
	}
}

func TestCoalescesIdenticalMisses(t *testing.T) {
	u := newUpstream(t, echo)
	u.gate = make(chan struct{})
	p := newProxy(t, u, NewMemoryCache())

	const n = 40
	var wg sync.WaitGroup
	labels := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec, body := post(p, `{"jsonrpc":"2.0","id":1,"method":"getAccountInfo","params":["hot"]}`)
			if !strings.Contains(body, `"result"`) {
				t.Errorf("no result: %s", body)
			}
			labels <- rec.Header().Get("X-Cache")
		}()
	}
	// Let every request reach the proxy while the first upstream call is held.
	deadline := time.Now().Add(2 * time.Second)
	for u.calls.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	close(u.gate)
	wg.Wait()
	close(labels)

	if c := u.calls.Load(); c != 1 {
		t.Fatalf("upstream calls = %d, want 1", c)
	}
	var miss, shared int
	for l := range labels {
		switch l {
		case "MISS":
			miss++
		case "COALESCED", "HIT":
			shared++
		}
	}
	if miss != 1 || shared != n-1 {
		t.Fatalf("miss=%d shared=%d", miss, shared)
	}
}

func TestTTLExpiry(t *testing.T) {
	u := newUpstream(t, echo)
	c := NewMemoryCache()
	now := time.Now()
	c.now = func() time.Time { return now }
	p := newProxy(t, u, c)
	req := `{"jsonrpc":"2.0","id":1,"method":"getAccountInfo","params":["a"]}`

	post(p, req)
	post(p, req)
	if u.calls.Load() != 1 {
		t.Fatalf("calls=%d", u.calls.Load())
	}
	now = now.Add(DefaultTTLs().State + time.Millisecond)
	post(p, req)
	if u.calls.Load() != 2 {
		t.Fatalf("entry did not expire: calls=%d", u.calls.Load())
	}
}

func TestBatch(t *testing.T) {
	u := newUpstream(t, echo)
	p := newProxy(t, u, NewMemoryCache())
	post(p, `{"jsonrpc":"2.0","id":9,"method":"getBalance","params":["warm"]}`)

	rec, body := post(p, `[
	 {"jsonrpc":"2.0","id":1,"method":"getBalance","params":["warm"]},
	 {"jsonrpc":"2.0","id":2,"method":"getBalance","params":["cold"]},
	 {"jsonrpc":"2.0","id":3,"method":"sendTransaction","params":["tx"]}]`)
	if !strings.HasPrefix(rec.Header().Get("X-Cache"), "BATCH hit=1 miss=1 bypass=1") {
		t.Fatalf("X-Cache = %q", rec.Header().Get("X-Cache"))
	}
	var arr []struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &arr); err != nil || len(arr) != 3 {
		t.Fatalf("batch reply: %v %s", err, body)
	}
	for i, want := range []string{"1", "2", "3"} {
		if string(arr[i].ID) != want {
			t.Fatalf("order broken: %s", body)
		}
	}
	_, body = post(p, `[]`)
	if !strings.Contains(body, "Invalid Request") {
		t.Fatalf("empty batch: %s", body)
	}
}

type brokenCache struct{ calls atomic.Int64 }

func (c *brokenCache) Get([]byte) ([]byte, bool, error) {
	c.calls.Add(1)
	return nil, false, errors.New("down")
}
func (c *brokenCache) Set([]byte, []byte, time.Duration) error {
	c.calls.Add(1)
	return errors.New("down")
}

func TestCacheFailureIsNotFatal(t *testing.T) {
	u := newUpstream(t, echo)
	bc := &brokenCache{}
	p := newProxy(t, u, bc)
	for i := 0; i < 20; i++ {
		_, body := post(p, `{"jsonrpc":"2.0","id":1,"method":"getBalance","params":["a"]}`)
		if !strings.Contains(body, `"result"`) {
			t.Fatalf("request failed with the cache down: %s", body)
		}
	}
	// After the first failure the cache is skipped for a while.
	if c := bc.calls.Load(); c > 2 {
		t.Fatalf("cache called %d times while down", c)
	}
	if p.Stats().CacheErrors == 0 {
		t.Fatal("cache errors not counted")
	}
}

func TestUpstreamHTTPErrorIsRelayedNotCached(t *testing.T) {
	u := newUpstream(t, echo)
	u.status = 429
	c := NewMemoryCache()
	p := newProxy(t, u, c)
	rec, body := post(p, `{"jsonrpc":"2.0","id":1,"method":"getBalance","params":["a"]}`)
	if rec.Code != 429 || !strings.Contains(body, "rate limited") {
		t.Fatalf("got %d %q", rec.Code, body)
	}
	if c.Len() != 0 {
		t.Fatal("error response was cached")
	}
}

func TestOversizeResultNotCached(t *testing.T) {
	big := `"` + strings.Repeat("x", 2000) + `"`
	u := newUpstream(t, func(string, json.RawMessage) (string, string) { return big, "" })
	c := NewMemoryCache()
	p, _ := New(Config{Chain: Solana, Upstream: u.srv.URL, Cache: c, MaxEntryBytes: 1000})
	_, body := post(p, `{"jsonrpc":"2.0","id":1,"method":"getBalance","params":["a"]}`)
	if !strings.Contains(body, strings.Repeat("x", 2000)) {
		t.Fatal("large result not served")
	}
	if c.Len() != 0 || p.Stats().TooLarge != 1 {
		t.Fatalf("len=%d tooLarge=%d", c.Len(), p.Stats().TooLarge)
	}
}

func TestHeadersForwardedAndMetrics(t *testing.T) {
	var got atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("X-Api-Key"))
		w.Write([]byte(`{"jsonrpc":"2.0","result":1,"id":1}`))
	}))
	defer srv.Close()
	p, _ := New(Config{Upstream: srv.URL, Cache: NewMemoryCache(), Headers: http.Header{"X-Api-Key": {"secret"}}})
	post(p, `{"jsonrpc":"2.0","id":1,"method":"getSlot","params":[]}`)
	post(p, `{"jsonrpc":"2.0","id":1,"method":"getSlot","params":[]}`)
	if got.Load() != "secret" {
		t.Fatalf("header = %v", got.Load())
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	m := rec.Body.String()
	if !strings.Contains(m, "rpccache_hits_total 1") || !strings.Contains(m, `rpccache_method_misses_total{method="getSlot"} 1`) {
		t.Fatalf("metrics:\n%s", m)
	}
	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != 200 {
		t.Fatal("healthz")
	}
}

func TestCacheKeyIgnoresWhitespaceNotContent(t *testing.T) {
	p, _ := New(Config{Upstream: "http://x", Cache: NewMemoryCache()})
	a := p.cacheKey("getBalance", json.RawMessage(`["a", {"b": 1}]`))
	b := p.cacheKey("getBalance", json.RawMessage(`["a",{"b":1}]`))
	c := p.cacheKey("getBalance", json.RawMessage(`["a",{"b":2}]`))
	d := p.cacheKey("getAccountInfo", json.RawMessage(`["a",{"b":1}]`))
	if a != b || a == c || a == d || len(a) != 17 {
		t.Fatalf("keys: %x %x %x %x", a, b, c, d)
	}
}
