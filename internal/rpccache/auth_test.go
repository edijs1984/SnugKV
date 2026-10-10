package rpccache

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"snugkv/internal/engine"
	"snugkv/internal/server"
)

// startSnugFns runs an in-process SnugKV with the built-in functions loaded.
func startSnugFns(t testing.TB) (string, *server.TCPServer) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	store, err := engine.NewWithOptions(engine.Options{Shards: 4, Encoding: true})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := server.Listen(addr, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.LoadBuiltinFunctions(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return addr, srv
}

type authEnv struct {
	up     *upstream
	auth   *Auth
	admin  *Admin
	client *Client
	secret string
	id     string
}

func newAuthEnv(t *testing.T, plan Plan, ttl time.Duration) *authEnv {
	t.Helper()
	addr, _ := startSnugFns(t)
	client := NewClient(addr, 4, time.Second)
	t.Cleanup(client.Close)
	admin := NewAdmin(client)
	if err := admin.SetPlan("p", plan); err != nil {
		t.Fatal(err)
	}
	secret, rec, err := admin.CreateKey("acme", "p")
	if err != nil {
		t.Fatal(err)
	}
	u := newUpstream(t, echo)
	proxy := newProxy(t, u, NewMemoryCache())
	a, err := NewAuth(AuthConfig{Store: client, RecordTTL: ttl, FlushEvery: time.Hour}, proxy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return &authEnv{up: u, auth: a, admin: admin, client: client, secret: secret, id: rec.ID}
}

func (e *authEnv) do(secret, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	rec := httptest.NewRecorder()
	e.auth.ServeHTTP(rec, req)
	return rec
}

const getSlot = `{"jsonrpc":"2.0","id":1,"method":"getSlot"}`

func TestAuthRejectsMissingAndUnknownKeys(t *testing.T) {
	e := newAuthEnv(t, Plan{RPS: 100, Burst: 100}, time.Second)
	if rec := e.do("", getSlot); rec.Code != 401 {
		t.Fatalf("no key: %d", rec.Code)
	}
	if rec := e.do("rk_nope", getSlot); rec.Code != 401 {
		t.Fatalf("unknown key: %d", rec.Code)
	}
	if e.up.calls.Load() != 0 {
		t.Fatal("rejected requests reached the node")
	}
	rec := e.do(e.secret, getSlot)
	if rec.Code != 200 || rec.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("valid key: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAuthKeyLocations(t *testing.T) {
	e := newAuthEnv(t, Plan{RPS: 100, Burst: 100}, time.Second)
	for name, mk := range map[string]func() *http.Request{
		"bearer": func() *http.Request {
			r := httptest.NewRequest("POST", "/", strings.NewReader(getSlot))
			r.Header.Set("Authorization", "bearer "+e.secret)
			return r
		},
		"x-api-key": func() *http.Request {
			r := httptest.NewRequest("POST", "/", strings.NewReader(getSlot))
			r.Header.Set("X-API-Key", e.secret)
			return r
		},
		"query": func() *http.Request {
			return httptest.NewRequest("POST", "/?api-key="+e.secret, strings.NewReader(getSlot))
		},
	} {
		rec := httptest.NewRecorder()
		e.auth.ServeHTTP(rec, mk())
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestAuthRateLimit(t *testing.T) {
	e := newAuthEnv(t, Plan{RPS: 1, Burst: 3}, time.Second)
	for i := 0; i < 3; i++ {
		if rec := e.do(e.secret, getSlot); rec.Code != 200 {
			t.Fatalf("call %d: %d", i, rec.Code)
		}
	}
	rec := e.do(e.secret, getSlot)
	if rec.Code != 429 {
		t.Fatalf("fourth call should be limited: %d", rec.Code)
	}
	if ra, _ := strconv.Atoi(rec.Header().Get("Retry-After")); ra < 1 {
		t.Fatalf("Retry-After %q", rec.Header().Get("Retry-After"))
	}
	if !strings.Contains(rec.Body.String(), "rate limit") {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestAuthBatchCostsItsSize(t *testing.T) {
	e := newAuthEnv(t, Plan{RPS: 1, Burst: 4}, time.Second)
	big := "[" + strings.Repeat(getSlot+",", 4) + getSlot + "]"
	if rec := e.do(e.secret, big); rec.Code != 429 || !strings.Contains(rec.Body.String(), "exceeds") {
		t.Fatalf("oversized batch: %d %s", rec.Code, rec.Body.String())
	}
	four := "[" + strings.Repeat(getSlot+",", 3) + getSlot + "]"
	if rec := e.do(e.secret, four); rec.Code != 200 {
		t.Fatalf("batch of burst size: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(e.secret, getSlot); rec.Code != 429 {
		t.Fatalf("bucket should be empty after the batch: %d", rec.Code)
	}
}

func TestAuthDailyQuota(t *testing.T) {
	e := newAuthEnv(t, Plan{RPS: 1000, Burst: 1000, Daily: 5}, time.Second)
	for i := 0; i < 5; i++ {
		if rec := e.do(e.secret, getSlot); rec.Code != 200 {
			t.Fatalf("call %d: %d", i, rec.Code)
		}
	}
	rec := e.do(e.secret, getSlot)
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "quota") {
		t.Fatalf("sixth call: %d %s", rec.Code, rec.Body.String())
	}
	if ra, _ := strconv.Atoi(rec.Header().Get("Retry-After")); ra < 1 || ra > 86401 {
		t.Fatalf("Retry-After %d", ra)
	}
	// A batch that does not fit is refused whole.
	if rec := e.do(e.secret, "["+getSlot+","+getSlot+"]"); rec.Code != 429 {
		t.Fatalf("batch over quota: %d", rec.Code)
	}
}

func TestAuthQuotaSurvivesRestart(t *testing.T) {
	e := newAuthEnv(t, Plan{RPS: 1000, Burst: 1000, Daily: 3}, time.Second)
	for i := 0; i < 3; i++ {
		e.do(e.secret, getSlot)
	}
	e.auth.Flush()

	// A fresh proxy process reads today's count from the store.
	proxy := newProxy(t, e.up, NewMemoryCache())
	a2, err := NewAuth(AuthConfig{Store: e.client, RecordTTL: time.Second, FlushEvery: time.Hour}, proxy)
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()
	req := httptest.NewRequest("POST", "/", strings.NewReader(getSlot))
	req.Header.Set("Authorization", "Bearer "+e.secret)
	rec := httptest.NewRecorder()
	a2.ServeHTTP(rec, req)
	if rec.Code != 429 {
		t.Fatalf("restarted proxy forgot usage: %d", rec.Code)
	}
}

func TestAuthMetersByClass(t *testing.T) {
	e := newAuthEnv(t, Plan{RPS: 1000, Burst: 1000}, time.Second)
	e.do(e.secret, getSlot)                                                                // tip
	e.do(e.secret, `{"jsonrpc":"2.0","id":2,"method":"getAccountInfo","params":["abc"]}`)  // state
	e.do(e.secret, `{"jsonrpc":"2.0","id":3,"method":"sendTransaction","params":["abc"]}`) // bypass
	e.do(e.secret, "["+getSlot+","+getSlot+"]")                                            // 2 tip
	e.auth.Flush()

	u, err := e.admin.Usage(e.id, 1)
	if err != nil {
		t.Fatal(err)
	}
	got := u[0].Counters
	want := map[string]int64{"calls": 5, "tip": 3, "state": 1, "bypass": 1}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
}

func TestAuthCountsDenied(t *testing.T) {
	e := newAuthEnv(t, Plan{RPS: 1, Burst: 1}, time.Second)
	e.do(e.secret, getSlot)
	e.do(e.secret, getSlot)
	e.auth.Flush()
	u, _ := e.admin.Usage(e.id, 1)
	if u[0].Counters["calls"] != 1 || u[0].Counters["denied"] != 1 {
		t.Fatalf("counters %v", u[0].Counters)
	}
}

func TestAuthRevocationAndPlanChangeTakeEffect(t *testing.T) {
	e := newAuthEnv(t, Plan{RPS: 1000, Burst: 1000}, 50*time.Millisecond)
	if rec := e.do(e.secret, getSlot); rec.Code != 200 {
		t.Fatalf("before: %d", rec.Code)
	}
	if err := e.admin.SetPlan("tiny", Plan{RPS: 1, Burst: 1}); err != nil {
		t.Fatal(err)
	}
	if err := e.admin.SetKeyPlan(e.id, "tiny"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	e.do(e.secret, getSlot)
	if rec := e.do(e.secret, getSlot); rec.Code != 429 {
		t.Fatalf("new plan not applied: %d", rec.Code)
	}
	if err := e.admin.Revoke(e.id); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if rec := e.do(e.secret, getSlot); rec.Code != 401 {
		t.Fatalf("revoked key still works: %d", rec.Code)
	}
}

func TestAuthStoreDown(t *testing.T) {
	addr, srv := startSnugFns(t)
	client := NewClient(addr, 2, 200*time.Millisecond)
	defer client.Close()
	admin := NewAdmin(client)
	admin.SetPlan("p", Plan{RPS: 100, Burst: 100})
	secret, _, _ := admin.CreateKey("x", "p")
	u := newUpstream(t, echo)
	a, _ := NewAuth(AuthConfig{Store: client, RecordTTL: 30 * time.Millisecond, FlushEvery: time.Hour}, newProxy(t, u, NewMemoryCache()))
	defer a.Close()
	call := func(s string) int {
		req := httptest.NewRequest("POST", "/", strings.NewReader(getSlot))
		req.Header.Set("Authorization", "Bearer "+s)
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := call(secret); c != 200 {
		t.Fatalf("before outage: %d", c)
	}
	srv.Close()
	client.Close()
	time.Sleep(60 * time.Millisecond)
	if c := call(secret); c != 200 {
		t.Fatalf("known key during outage should keep working: %d", c)
	}
	if c := call("rk_never_seen"); c != 503 {
		t.Fatalf("unknown key during outage should be 503, got %d", c)
	}
}

func TestAuthPassesThroughHealthAndMetrics(t *testing.T) {
	e := newAuthEnv(t, Plan{RPS: 100, Burst: 100}, time.Second)
	e.do("nope", getSlot)
	for _, path := range []string{"/healthz", "/metrics"} {
		rec := httptest.NewRecorder()
		e.auth.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		if path == "/metrics" && !strings.Contains(rec.Body.String(), `rpcauth_requests_total{result="unauthorized"} 1`) {
			t.Fatalf("metrics: %s", rec.Body.String())
		}
	}
}

func TestAdminListsKeysAndPlans(t *testing.T) {
	e := newAuthEnv(t, Plan{RPS: 5, Burst: 10, Daily: 100}, time.Second)
	if _, _, err := e.admin.CreateKey("second", "p"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.admin.CreateKey("bad", "nosuchplan"); err == nil {
		t.Fatal("key on a missing plan should fail")
	}
	keys, err := e.admin.Keys()
	if err != nil || len(keys) != 2 {
		t.Fatalf("keys %v err %v", keys, err)
	}
	plans, err := e.admin.Plans()
	if err != nil || plans["p"].Burst != 10 || plans["p"].Daily != 100 {
		t.Fatalf("plans %v err %v", plans, err)
	}
	if err := e.admin.SetPlan("bad name", Plan{RPS: 1, Burst: 1}); err == nil {
		t.Fatal("bad plan name accepted")
	}
	if err := e.admin.SetPlan("x", Plan{RPS: 0, Burst: 1}); err == nil {
		t.Fatal("zero rps accepted")
	}
	// The secret is not stored anywhere.
	v, _ := e.client.Do("KEYS", "*")
	for _, k := range v.Array {
		if strings.Contains(k.Str, e.secret) {
			t.Fatal("secret appears in a key name")
		}
	}
	rec, _, _ := e.admin.GetKey(e.id)
	if strings.Contains(rec.Label+rec.Plan, e.secret) {
		t.Fatal("secret stored in record")
	}
}

// BenchmarkAuthOverhead measures what the key check, rate limit and metering
// add to a cached request (the proxy answers from an in-memory cache).
func BenchmarkAuthOverhead(b *testing.B) {
	addr, _ := startSnugFns(b)
	client := NewClient(addr, 64, time.Second)
	defer client.Close()
	admin := NewAdmin(client)
	admin.SetPlan("p", Plan{RPS: 1e9, Burst: 1 << 30})
	secret, _, _ := admin.CreateKey("bench", "p")
	u := newUpstream(b, echo)
	proxy, _ := New(Config{Chain: Solana, Upstream: u.srv.URL, Cache: NewMemoryCache()})
	a, _ := NewAuth(AuthConfig{Store: client}, proxy)
	defer a.Close()
	body := `{"jsonrpc":"2.0","id":1,"method":"getGenesisHash"}`
	serve := func(h http.Handler, key string) {
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	serve(proxy, "") // warm the cache
	for name, h := range map[string]struct {
		h   http.Handler
		key string
	}{"proxy-only": {proxy, ""}, "with-auth": {a, secret}} {
		b.Run(name, func(b *testing.B) {
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					serve(h.h, h.key)
				}
			})
		})
	}
}
