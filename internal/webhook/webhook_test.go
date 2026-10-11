package webhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"snugkv/internal/config"
	"snugkv/internal/engine"
	"snugkv/internal/server"
)

func TestSealerRoundTrip(t *testing.T) {
	s, err := newSealer("0123456789abcdef-key")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := s.seal("sk_live_secret")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(enc, "sk_live") || !strings.HasPrefix(enc, encPrefix) {
		t.Fatalf("not encrypted: %s", enc)
	}
	if got, err := s.open(enc); err != nil || got != "sk_live_secret" {
		t.Fatalf("open = %q, %v", got, err)
	}
	other, _ := newSealer("another-key-0123456789")
	if _, err := other.open(enc); err == nil {
		t.Fatal("wrong key must not decrypt")
	}
	if _, err := (*sealer)(nil).seal("x"); err == nil {
		t.Fatal("sealing without a key must fail")
	}
	if _, err := newSealer("short"); err == nil {
		t.Fatal("short key must be rejected")
	}
}

func TestBlockedIP(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "0.0.0.0", "::1", "fe80::1", "100.64.0.1", "224.0.0.1", "fc00::1"} {
		if !blockedIP(net.ParseIP(ip)) {
			t.Errorf("%s should be blocked", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "93.184.216.34", "2606:4700:4700::1111"} {
		if blockedIP(net.ParseIP(ip)) {
			t.Errorf("%s should be allowed", ip)
		}
	}
}

func TestNormalizeRejectsBadInput(t *testing.T) {
	bad := []Definition{
		{Name: "", Kind: KindInbound, Topic: "t"},
		{Name: "a", Kind: "x", Topic: "t"},
		{Name: "a", Kind: KindInbound, Topic: "has space"},
		{Name: "a", Kind: KindOutbound, Topic: "t", Outbound: &Outbound{URL: "ftp://x/y"}},
		{Name: "a", Kind: KindOutbound, Topic: "t", Outbound: &Outbound{URL: "https://user:pw@x.test/"}},
		{Name: "a", Kind: KindOutbound, Topic: "t", Outbound: &Outbound{URL: "https://x.test/", Headers: []Header{{Name: "Host", Value: "evil"}}}},
		{Name: "a", Kind: KindOutbound, Topic: "t", Outbound: &Outbound{URL: "https://x.test/", Headers: []Header{{Name: "X-A", Value: "a\r\nX-B: b"}}}},
		{Name: "a", Kind: KindOutbound, Topic: "t", Outbound: &Outbound{URL: "https://x.test/", Method: "DELETE"}},
		{Name: "a", Kind: KindInbound, Topic: "t", Inbound: &Inbound{Auth: InboundAuth{Mode: "magic"}}},
	}
	for i, d := range bad {
		if err := d.normalize(); err == nil {
			t.Errorf("case %d should be rejected", i)
		}
	}
	d := Definition{Name: "ok", Kind: KindOutbound, Topic: "t", Outbound: &Outbound{URL: "https://x.test/hook"}}
	if err := d.normalize(); err != nil {
		t.Fatal(err)
	}
	if d.Outbound.MaxAttempts != 5 || d.Outbound.Method != "POST" || d.Outbound.From != "$" || d.Outbound.Body != "envelope" {
		t.Fatalf("defaults not applied: %+v", d.Outbound)
	}
}

func TestSecretsAreSealedRedactedAndKept(t *testing.T) {
	s, _ := newSealer("0123456789abcdef-key")
	d := Definition{ID: "x", Name: "n", Kind: KindOutbound, Topic: "t", Outbound: &Outbound{
		URL:           "https://x.test/",
		SigningSecret: SecretField{Plain: "whsec_abcdef"},
		Headers:       []Header{{Name: "X-Api-Key", Value: "key-123456", Secret: true}, {Name: "X-Plain", Value: "visible"}},
	}}
	if err := d.normalize(); err != nil {
		t.Fatal(err)
	}
	if err := d.sealSecrets(s, nil); err != nil {
		t.Fatal(err)
	}
	stored, _ := json.Marshal(d)
	for _, leak := range []string{"whsec_abcdef", "key-123456"} {
		if strings.Contains(string(stored), leak) {
			t.Fatalf("stored form leaks %q: %s", leak, stored)
		}
	}
	shown, _ := json.Marshal(d.redacted())
	if strings.Contains(string(shown), "enc:v1") || strings.Contains(string(shown), "key-123456") {
		t.Fatalf("API form leaks: %s", shown)
	}
	if !strings.Contains(string(shown), "••••3456") || !strings.Contains(string(shown), "visible") {
		t.Fatalf("API form should show hints and plain headers: %s", shown)
	}
	// An edit that omits the secrets keeps them.
	edit := Definition{ID: "x", Name: "n2", Kind: KindOutbound, Topic: "t", Outbound: &Outbound{
		URL:     "https://x.test/",
		Headers: []Header{{Name: "x-api-key", Secret: true}},
	}}
	if err := edit.normalize(); err != nil {
		t.Fatal(err)
	}
	if err := edit.sealSecrets(s, &d); err != nil {
		t.Fatal(err)
	}
	if edit.Outbound.Headers[0].Enc != d.Outbound.Headers[0].Enc || edit.Outbound.SigningSecret.Enc != d.Outbound.SigningSecret.Enc {
		t.Fatal("omitted secrets should be kept")
	}
	// A new secret header with no value is an error.
	bad := Definition{Name: "n", Kind: KindOutbound, Topic: "t", Outbound: &Outbound{URL: "https://x.test/", Headers: []Header{{Name: "X-New", Secret: true}}}}
	bad.normalize()
	if err := bad.sealSecrets(s, nil); err == nil {
		t.Fatal("secret header without value must fail")
	}
}

// ---- integration against a real in-process server

type env struct {
	t       *testing.T
	kvAddr  string
	kv      *kvClient
	m       *Manager
	pub     string
	api     string
	srv     *server.TCPServer
	apiTok  string
	private bool
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := l.Addr().String()
	l.Close()
	return a
}

func newEnv(t *testing.T, allowPrivate bool) *env {
	t.Helper()
	cfg := config.Default()
	cfg.ListenAddr = freeAddr(t)
	cfg.AdminAddr = freeAddr(t)
	srv, err := server.ListenWithConfig(cfg, engine.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	e := &env{t: t, kvAddr: cfg.ListenAddr, srv: srv, apiTok: "tok-0123456789"}
	e.kv = newKVClient(cfg.ListenAddr, "", "", 4, 5*time.Second)
	m, err := Start(Options{
		KVAddr: cfg.ListenAddr, PublicListen: "127.0.0.1:0", APIListen: "127.0.0.1:0", APIToken: e.apiTok,
		Key: "0123456789abcdef-key", AllowPrivate: allowPrivate, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	e.m = m
	addrs := m.Addrs()
	e.pub, e.api = "http://"+addrs[0], "http://"+addrs[1]
	return e
}

func (e *env) call(method, path string, body any) (int, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.api+path, rd)
	req.Header.Set("Authorization", "Bearer "+e.apiTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	b, _ := io.ReadAll(resp.Body)
	json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

func (e *env) do(args ...string) value {
	e.t.Helper()
	v, err := e.kv.do(args...)
	if err != nil {
		e.t.Fatalf("%v: %v", args, err)
	}
	return v
}

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for !cond() {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

type received struct {
	mu   sync.Mutex
	reqs []recvReq
}
type recvReq struct {
	h    http.Header
	body []byte
}

func (r *received) add(q *http.Request) {
	b, _ := io.ReadAll(q.Body)
	r.mu.Lock()
	r.reqs = append(r.reqs, recvReq{q.Header.Clone(), b})
	r.mu.Unlock()
}
func (r *received) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.reqs) }
func (r *received) at(i int) recvReq {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reqs[i]
}

func TestAPIAuthAndSecretsNeverReturned(t *testing.T) {
	e := newEnv(t, true)
	resp, _ := http.Get(e.api + "/v1/webhooks")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	code, out := e.call("POST", "/v1/webhooks", map[string]any{
		"name": "stripe", "kind": "inbound", "topic": "payments", "enabled": true,
		"inbound": map[string]any{"auth": map[string]any{"mode": "hmac", "header": "X-Sig", "prefix": "sha256=", "secret": map[string]any{"value": "whsec_supersecret"}}},
	})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, out)
	}
	id := out["id"].(string)
	_, list := e.call("GET", "/v1/webhooks", nil)
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "supersecret") || strings.Contains(string(raw), "enc:v1") {
		t.Fatalf("secret leaked: %s", raw)
	}
	// And at rest in the store.
	if stored := e.do("GET", keyDef+id).str; strings.Contains(stored, "supersecret") {
		t.Fatalf("secret stored in the clear: %s", stored)
	}
	// Edit without the secret: it must survive.
	code, _ = e.call("PUT", "/v1/webhooks/"+id, map[string]any{
		"name": "stripe2", "topic": "payments", "enabled": true,
		"inbound": map[string]any{"auth": map[string]any{"mode": "hmac", "header": "X-Sig", "prefix": "sha256="}},
	})
	if code != http.StatusOK {
		t.Fatalf("update: %d", code)
	}
	body := []byte(`{"amount":5}`)
	mac := hmac.New(sha256.New, []byte("whsec_supersecret"))
	mac.Write(body)
	req, _ := http.NewRequest("POST", e.pub+"/hook/"+id, bytes.NewReader(body))
	req.Header.Set("X-Sig", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusAccepted {
		t.Fatalf("signed post after edit: %v %v", resp, err)
	}
}

func TestInboundPublishesEnvelope(t *testing.T) {
	e := newEnv(t, true)
	_, out := e.call("POST", "/v1/webhooks", map[string]any{
		"name": "gh", "kind": "inbound", "topic": "github.events", "enabled": true,
		"inbound": map[string]any{"eventHeader": "X-GitHub-Event", "maxLen": 100, "auth": map[string]any{"mode": "token", "header": "X-Token", "secret": map[string]any{"value": "tok-abcdef123456"}}},
	})
	id := out["id"].(string)
	url := e.pub + "/hook/" + id

	post := func(token, body string) int {
		req, _ := http.NewRequest("POST", url, strings.NewReader(body))
		req.Header.Set("X-Token", token)
		req.Header.Set("X-GitHub-Event", "push")
		req.Header.Set("X-Custom", "yes")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := post("wrong", `{"a":1}`); c != 401 {
		t.Fatalf("bad token = %d", c)
	}
	if c := post("tok-abcdef123456", `{"ref":"main","n":1}`); c != 202 {
		t.Fatalf("good token = %d", c)
	}
	if c := post("tok-abcdef123456", `plain text, not json`); c != 202 {
		t.Fatalf("text body = %d", c)
	}
	if c := post("tok-abcdef123456", ``); c != 400 {
		t.Fatalf("empty body = %d", c)
	}
	if n := e.do("XLEN", "github.events").n; n != 2 {
		t.Fatalf("stream length = %d, want 2 (rejected requests must not publish)", n)
	}
	entries := parseEntries(e.do("XRANGE", "github.events", "-", "+"))
	f := entries[0].fields
	if f["payload"] != `{"ref":"main","n":1}` || f["event"] != "push" || !strings.HasPrefix(f["producer"], "webhook:gh") || f["mid"] == "" || f["ts"] == "" {
		t.Fatalf("envelope: %v", f)
	}
	if !strings.Contains(f["headers"], `"x-custom":"yes"`) || strings.Contains(strings.ToLower(f["headers"]), "token") {
		t.Fatalf("headers kept wrongly: %s", f["headers"])
	}
	if entries[1].fields["payload"] != `"plain text, not json"` {
		t.Fatalf("text payload: %q", entries[1].fields["payload"])
	}
	// unknown id, wrong method
	resp, _ := http.Post(e.pub+"/hook/aaaaaaaaaaaaaaaa", "application/json", strings.NewReader("{}"))
	if resp.StatusCode != 404 {
		t.Fatalf("unknown id = %d", resp.StatusCode)
	}
	resp, _ = http.Get(url)
	if resp.StatusCode != 405 {
		t.Fatalf("GET = %d", resp.StatusCode)
	}
}

func TestInboundRateLimit(t *testing.T) {
	e := newEnv(t, true)
	_, out := e.call("POST", "/v1/webhooks", map[string]any{
		"name": "rl", "kind": "inbound", "topic": "rl.topic", "enabled": true,
		"inbound": map[string]any{"maxPerSecond": 2},
	})
	url := e.pub + "/hook/" + out["id"].(string)
	got429 := false
	for i := 0; i < 20; i++ {
		resp, _ := http.Post(url, "application/json", strings.NewReader("{}"))
		if resp.StatusCode == 429 {
			got429 = true
		}
		resp.Body.Close()
	}
	if !got429 {
		t.Fatal("expected a 429 once the burst is used up")
	}
}

func TestOutboundDeliversWithHeadersAndSignature(t *testing.T) {
	e := newEnv(t, true)
	var rec received
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { rec.add(r); w.WriteHeader(200) }))
	defer ts.Close()
	code, out := e.call("POST", "/v1/webhooks", map[string]any{
		"name": "to-crm", "kind": "outbound", "topic": "orders.created", "enabled": true,
		"outbound": map[string]any{
			"url": ts.URL + "/in", "signingSecret": map[string]any{"value": "sign-secret-1"},
			"headers": []map[string]any{{"name": "X-Api-Key", "value": "key-abcdef", "secret": true}, {"name": "X-Team", "value": "ops"}},
		},
	})
	if code != 201 {
		t.Fatalf("create: %d %v", code, out)
	}
	id := out["id"].(string)
	waitFor(t, 5*time.Second, "worker", func() bool { _, o := e.call("GET", "/v1/webhooks/"+id, nil); return o["running"] == true })
	e.do("XADD", "orders.created", "*", "payload", `{"orderId":7}`, "event", "order.created", "mid", "m-1", "ts", "1700000000000", "producer", "billing")
	waitFor(t, 5*time.Second, "delivery", func() bool { return rec.count() == 1 })
	r := rec.at(0)
	if r.h.Get("X-Api-Key") != "key-abcdef" || r.h.Get("X-Team") != "ops" || r.h.Get("X-Snugkv-Message-Id") != "m-1" ||
		r.h.Get("X-Snugkv-Event") != "order.created" || r.h.Get("X-Snugkv-Delivery-Attempt") != "1" || r.h.Get("X-Snugkv-Webhook-Id") != id {
		t.Fatalf("headers: %v", r.h)
	}
	if want := signature("sign-secret-1", r.h.Get("X-Snugkv-Timestamp"), r.body); r.h.Get("X-Snugkv-Signature") != want {
		t.Fatalf("signature %q, want %q", r.h.Get("X-Snugkv-Signature"), want)
	}
	var env map[string]any
	if err := json.Unmarshal(r.body, &env); err != nil {
		t.Fatal(err)
	}
	p := env["payload"].(map[string]any)
	if p["orderId"] != float64(7) || env["topic"] != "orders.created" || env["mid"] != "m-1" || env["producer"] != "billing" {
		t.Fatalf("body: %s", r.body)
	}
	// Acknowledged: nothing pending.
	waitFor(t, 3*time.Second, "ack", func() bool {
		v := e.do("XPENDING", "orders.created", "webhook:"+id)
		return len(v.arr) > 0 && v.arr[0].n == 0
	})
	_, logs := e.call("GET", "/v1/webhooks/"+id+"/log", nil)
	if es, _ := logs["entries"].([]any); len(es) == 0 {
		t.Fatal("expected a delivery log entry")
	}
}

func TestOutboundRetriesThenSucceeds(t *testing.T) {
	e := newEnv(t, true)
	var rec received
	var mu sync.Mutex
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n < 2 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}))
	defer ts.Close()
	_, out := e.call("POST", "/v1/webhooks", map[string]any{
		"name": "flaky", "kind": "outbound", "topic": "flaky.topic", "enabled": true, "outbound": map[string]any{"url": ts.URL},
	})
	id := out["id"].(string)
	waitFor(t, 5*time.Second, "worker", func() bool { _, o := e.call("GET", "/v1/webhooks/"+id, nil); return o["running"] == true })
	e.do("XADD", "flaky.topic", "*", "payload", `{"x":1}`)
	waitFor(t, 8*time.Second, "retry", func() bool { return rec.count() == 2 })
	if rec.at(1).h.Get("X-Snugkv-Delivery-Attempt") != "2" {
		t.Fatalf("attempt header = %q", rec.at(1).h.Get("X-Snugkv-Delivery-Attempt"))
	}
	if n := e.do("XLEN", "flaky.topic.dlq").n; n != 0 {
		t.Fatalf("dlq should be empty, has %d", n)
	}
}

func TestOutboundPermanentFailureGoesToDeadLetters(t *testing.T) {
	e := newEnv(t, true)
	var rec received
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { rec.add(r); w.WriteHeader(400) }))
	defer ts.Close()
	_, out := e.call("POST", "/v1/webhooks", map[string]any{
		"name": "reject", "kind": "outbound", "topic": "bad.topic", "enabled": true, "outbound": map[string]any{"url": ts.URL},
	})
	id := out["id"].(string)
	waitFor(t, 5*time.Second, "worker", func() bool { _, o := e.call("GET", "/v1/webhooks/"+id, nil); return o["running"] == true })
	e.do("XADD", "bad.topic", "*", "payload", `{"x":1}`, "mid", "bad-1")
	waitFor(t, 5*time.Second, "dead letter", func() bool { return e.do("XLEN", "bad.topic.dlq").n == 1 })
	if rec.count() != 1 {
		t.Fatalf("a 400 must not be retried, got %d requests", rec.count())
	}
	f := parseEntries(e.do("XRANGE", "bad.topic.dlq", "-", "+"))[0].fields
	if f["mid"] != "bad-1" || f["dlq_error"] != "HTTP 400" || f["dlq_webhook"] != id {
		t.Fatalf("dlq entry: %v", f)
	}
	waitFor(t, 3*time.Second, "ack", func() bool {
		v := e.do("XPENDING", "bad.topic", "webhook:"+id)
		return len(v.arr) > 0 && v.arr[0].n == 0
	})
}

func TestOutboundBlocksPrivateAddressesByDefault(t *testing.T) {
	e := newEnv(t, false)
	var rec received
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { rec.add(r) }))
	defer ts.Close() // listens on 127.0.0.1, which must be refused
	_, out := e.call("POST", "/v1/webhooks", map[string]any{
		"name": "ssrf", "kind": "outbound", "topic": "ssrf.topic", "enabled": true, "outbound": map[string]any{"url": ts.URL},
	})
	id := out["id"].(string)
	waitFor(t, 5*time.Second, "worker", func() bool { _, o := e.call("GET", "/v1/webhooks/"+id, nil); return o["running"] == true })
	e.do("XADD", "ssrf.topic", "*", "payload", `1`)
	waitFor(t, 5*time.Second, "dead letter", func() bool { return e.do("XLEN", "ssrf.topic.dlq").n == 1 })
	if rec.count() != 0 {
		t.Fatal("the private destination must never be contacted")
	}
	code, res := e.call("POST", "/v1/webhooks/"+id+"/test", nil)
	if code != 200 || res["ok"] != false || !strings.Contains(res["error"].(string), "not allowed") {
		t.Fatalf("test button: %d %v", code, res)
	}
}

func TestOutboundResumesPendingAfterRestart(t *testing.T) {
	e := newEnv(t, true)
	var rec received
	fail := true
	var mu sync.Mutex
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		mu.Lock()
		f := fail
		mu.Unlock()
		if f {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
	}))
	defer ts.Close()
	_, out := e.call("POST", "/v1/webhooks", map[string]any{
		"name": "resume", "kind": "outbound", "topic": "resume.topic", "enabled": true, "outbound": map[string]any{"url": ts.URL, "maxAttempts": 10},
	})
	id := out["id"].(string)
	waitFor(t, 5*time.Second, "worker", func() bool { _, o := e.call("GET", "/v1/webhooks/"+id, nil); return o["running"] == true })
	e.do("XADD", "resume.topic", "*", "payload", `{"n":1}`, "mid", "r-1")
	waitFor(t, 5*time.Second, "first attempt", func() bool { return rec.count() >= 1 })
	// Disable (stops the worker mid-retry; the message stays pending), fix the receiver, enable again.
	_, cur := e.call("GET", "/v1/webhooks/"+id, nil)
	cur["enabled"] = false
	delete(cur, "stats")
	for _, k := range []string{"running", "hookUrl", "group", "createdMs", "updatedMs", "id"} {
		delete(cur, k)
	}
	if c, o := e.call("PUT", "/v1/webhooks/"+id, cur); c != 200 {
		t.Fatalf("disable: %d %v", c, o)
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	cur["enabled"] = true
	if c, o := e.call("PUT", "/v1/webhooks/"+id, cur); c != 200 {
		t.Fatalf("enable: %d %v", c, o)
	}
	waitFor(t, 8*time.Second, "redelivery", func() bool {
		v := e.do("XPENDING", "resume.topic", "webhook:"+id)
		return len(v.arr) > 0 && v.arr[0].n == 0
	})
	last := rec.at(rec.count() - 1)
	if last.h.Get("X-Snugkv-Message-Id") != "r-1" {
		t.Fatalf("redelivered message id %q", last.h.Get("X-Snugkv-Message-Id"))
	}
	if n := e.do("XLEN", "resume.topic.dlq").n; n != 0 {
		t.Fatalf("nothing should be dead-lettered, got %d", n)
	}
}

func TestDeleteRemovesEverything(t *testing.T) {
	e := newEnv(t, true)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()
	_, out := e.call("POST", "/v1/webhooks", map[string]any{
		"name": "gone", "kind": "outbound", "topic": "gone.topic", "enabled": true, "outbound": map[string]any{"url": ts.URL},
	})
	id := out["id"].(string)
	waitFor(t, 5*time.Second, "worker", func() bool { _, o := e.call("GET", "/v1/webhooks/"+id, nil); return o["running"] == true })
	if c, _ := e.call("DELETE", "/v1/webhooks/"+id, nil); c != 200 {
		t.Fatalf("delete: %d", c)
	}
	if c, _ := e.call("GET", "/v1/webhooks/"+id, nil); c != 404 {
		t.Fatalf("after delete: %d", c)
	}
	if e.do("EXISTS", keyDef+id).n != 0 {
		t.Fatal("definition should be gone")
	}
	if g := e.do("XINFO", "GROUPS", "gone.topic"); len(g.arr) != 0 {
		t.Fatalf("consumer group should be dropped, have %d", len(g.arr))
	}
}
