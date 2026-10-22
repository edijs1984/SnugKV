package secrets

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"snugkv/internal/config"
	"snugkv/internal/engine"
	"snugkv/internal/server"
)

const testKey = "0123456789abcdef-secrets"

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

type env struct {
	t      *testing.T
	kvAddr string
	m      *Manager
	api    string
	tok    string
	kv     *kvClient
}

func startKV(t *testing.T) string {
	t.Helper()
	cfg := config.Default()
	cfg.ListenAddr = freeAddr(t)
	cfg.AdminAddr = freeAddr(t)
	srv, err := server.ListenWithConfig(cfg, engine.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return cfg.ListenAddr
}

func newEnv(t *testing.T) *env {
	t.Helper()
	addr := startKV(t)
	e := &env{t: t, kvAddr: addr, tok: "admin-token-0123456789abcdef"}
	m, err := Start(Options{KVAddr: addr, APIListen: "127.0.0.1:0", APIToken: e.tok, Key: testKey, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	e.m, e.api = m, "http://"+m.Addr()
	e.kv = newKVClient(addr, "", "", 2, 5*time.Second)
	t.Cleanup(e.kv.close)
	return e
}

func (e *env) req(token, method, path string, body any) (int, map[string]any, string) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.api+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	json.Unmarshal(b, &out)
	return resp.StatusCode, out, string(b)
}

func (e *env) admin(method, path string, body any) (int, map[string]any) {
	e.t.Helper()
	code, out, _ := e.req(e.tok, method, path, body)
	return code, out
}

func (e *env) must(method, path string, body any, want int) map[string]any {
	e.t.Helper()
	code, out, raw := e.req(e.tok, method, path, body)
	if code != want {
		e.t.Fatalf("%s %s = %d, want %d: %s", method, path, code, want, raw)
	}
	return out
}

func TestSealerBindsValuesToTheirPlace(t *testing.T) {
	s, err := newSealer(testKey)
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := s.seal("sk_live_123", bind("a/prod", "STRIPE"))
	if strings.Contains(enc, "sk_live") {
		t.Fatal("not encrypted")
	}
	if got, err := s.open(enc, bind("a/prod", "STRIPE")); err != nil || got != "sk_live_123" {
		t.Fatalf("open = %q %v", got, err)
	}
	if _, err := s.open(enc, bind("a/prod", "OTHER")); err == nil {
		t.Fatal("a value moved to another name must not open")
	}
	if _, err := s.open(enc, bind("a/dev", "STRIPE")); err == nil {
		t.Fatal("a value moved to another environment must not open")
	}
	other, _ := newSealer("another-key-0123456789")
	if _, err := other.open(enc, bind("a/prod", "STRIPE")); err == nil {
		t.Fatal("wrong key must not decrypt")
	}
	if _, err := newSealer("short"); err == nil {
		t.Fatal("short key must be rejected")
	}
}

func TestDotenvRoundTrip(t *testing.T) {
	in := "# comment\nexport A=1\nB = plain value # trailing\nC='single $NOT \\n'\nD=\"line1\\nline2 \\\"q\\\" \\$x\"\nE=\"multi\nline\"\nF=\n"
	got, err := ParseDotenv(in)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "1", "B": "plain value", "C": `single $NOT \n`, "D": "line1\nline2 \"q\" $x", "E": "multi\nline", "F": ""}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	back, err := ParseDotenv(FormatDotenv(got))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range got {
		if back[k] != v {
			t.Errorf("round trip %s = %q, want %q", k, back[k], v)
		}
	}
	for _, bad := range []string{"NOEQUALS", "1BAD=x", "A=\"open", "A='open"} {
		if _, err := ParseDotenv(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestStartRefusesUnsafeConfig(t *testing.T) {
	addr := startKV(t)
	cases := []Options{
		{KVAddr: addr, APIListen: "0.0.0.0:0", Key: testKey},                            // public without token
		{KVAddr: addr, APIListen: "127.0.0.1:0"},                                        // no key
		{KVAddr: addr, APIListen: "127.0.0.1:0", Key: "short"},                          // weak key
		{KVAddr: addr, APIListen: "127.0.0.1:0", Key: testKey, APIToken: "sks_abc_def"}, // admin token shaped like a read token
		{KVAddr: addr, APIListen: "127.0.0.1:0", Key: testKey, APIToken: "too-short-token"},
		{KVAddr: addr, APIListen: "127.0.0.1:0", Key: testKey, OldKey: testKey}, // old = new
		{KVAddr: addr, APIListen: "127.0.0.1:0", Key: testKey, OldKey: "short"},
	}
	for i, o := range cases {
		if m, err := Start(o); err == nil {
			m.Close()
			t.Errorf("case %d should be refused", i)
		}
	}
}

func TestWrongKeyIsRefusedAtStart(t *testing.T) {
	e := newEnv(t)
	if _, err := Start(Options{KVAddr: e.kvAddr, APIListen: "127.0.0.1:0", Key: "a-different-key-0123456789"}); err == nil {
		t.Fatal("a key that does not match the stored data must be refused")
	}
	m, err := Start(Options{KVAddr: e.kvAddr, APIListen: "127.0.0.1:0", Key: testKey})
	if err != nil {
		t.Fatalf("the right key must start: %v", err)
	}
	m.Close()
}

func TestSecretsLifecycle(t *testing.T) {
	e := newEnv(t)

	if code, _ := e.admin("GET", "/v1/envs", nil); code != 200 {
		t.Fatal(code)
	}
	if code, _, _ := e.req("", "GET", "/v1/envs", nil); code != 401 {
		t.Fatalf("no token = %d", code)
	}
	if code, _, _ := e.req("wrong", "GET", "/v1/envs", nil); code != 401 {
		t.Fatalf("wrong token = %d", code)
	}

	e.must("PUT", "/v1/envs/airsig/prod", nil, 201)
	e.must("PUT", "/v1/envs/airsig/prod", nil, 200) // idempotent
	if code, _ := e.admin("PUT", "/v1/envs/AirSig/prod", nil); code != 400 {
		t.Fatalf("bad env name = %d", code)
	}

	out := e.must("PUT", "/v1/envs/airsig/prod/secrets/MONGO_URI", map[string]string{"value": "mongodb://u:p@db/airsig"}, 200)
	if out["version"].(float64) != 1 || out["changed"] != true {
		t.Fatalf("first set = %v", out)
	}
	out = e.must("PUT", "/v1/envs/airsig/prod/secrets/MONGO_URI", map[string]string{"value": "mongodb://u:p@db/airsig"}, 200)
	if out["changed"] != false {
		t.Fatalf("same value must not make a version: %v", out)
	}
	e.must("PUT", "/v1/envs/airsig/prod/secrets/MONGO_URI", map[string]string{"value": "mongodb://u:p2@db/airsig"}, 200)
	if code, _ := e.admin("PUT", "/v1/envs/airsig/prod/secrets/bad-name", map[string]string{"value": "x"}); code != 400 {
		t.Fatalf("bad secret name = %d", code)
	}

	// Stored as ciphertext only
	raw, err := e.kv.do("HGET", keyEnv+"airsig/prod", "MONGO_URI")
	if err != nil || strings.Contains(raw.str, "mongodb") || !strings.Contains(raw.str, encPrefix) {
		t.Fatalf("stored in the clear: %q %v", raw.str, err)
	}

	// Listing hides values unless revealed
	list := e.must("GET", "/v1/envs/airsig/prod/secrets", nil, 200)["secrets"].([]any)
	s0 := list[0].(map[string]any)
	if s0["value"] != nil || s0["version"].(float64) != 2 || !strings.HasPrefix(s0["hint"].(string), "••••") {
		t.Fatalf("listing = %v", s0)
	}
	list = e.must("GET", "/v1/envs/airsig/prod/secrets?reveal=1", nil, 200)["secrets"].([]any)
	if list[0].(map[string]any)["value"] != "mongodb://u:p2@db/airsig" {
		t.Fatalf("reveal = %v", list)
	}

	// History and rollback
	vs := e.must("GET", "/v1/envs/airsig/prod/secrets/MONGO_URI/versions", nil, 200)["versions"].([]any)
	if len(vs) != 2 || vs[0].(map[string]any)["version"].(float64) != 2 {
		t.Fatalf("versions = %v", vs)
	}
	out = e.must("POST", "/v1/envs/airsig/prod/secrets/MONGO_URI/rollback", map[string]int{"version": 1}, 200)
	if out["version"].(float64) != 3 {
		t.Fatalf("rollback = %v", out)
	}
	got := e.must("GET", "/v1/envs/airsig/prod/secrets/MONGO_URI", nil, 200)
	if got["value"] != "mongodb://u:p@db/airsig" {
		t.Fatalf("after rollback = %v", got)
	}
	if code, _ := e.admin("POST", "/v1/envs/airsig/prod/secrets/MONGO_URI/rollback", map[string]int{"version": 99}); code != 400 {
		t.Fatalf("unknown version = %d", code)
	}

	// Delete keeps history and the version numbers keep going
	e.must("DELETE", "/v1/envs/airsig/prod/secrets/MONGO_URI", nil, 200)
	if code, _ := e.admin("GET", "/v1/envs/airsig/prod/secrets/MONGO_URI", nil); code != 404 {
		t.Fatalf("deleted secret = %d", code)
	}
	out = e.must("PUT", "/v1/envs/airsig/prod/secrets/MONGO_URI", map[string]string{"value": "again"}, 200)
	if out["version"].(float64) != 5 {
		t.Fatalf("versions must not repeat after a delete: %v", out)
	}

	// Audit
	entries := e.must("GET", "/v1/audit?env=airsig/prod", nil, 200)["entries"].([]any)
	actions := map[string]bool{}
	for _, a := range entries {
		actions[a.(map[string]any)["action"].(string)] = true
	}
	for _, a := range []string{"env.create", "set", "read", "reveal.all", "rollback", "delete"} {
		if !actions[a] {
			t.Errorf("audit misses %s: %v", a, entries)
		}
	}

	// Deleting the environment removes everything under it
	e.must("DELETE", "/v1/envs/airsig/prod", nil, 200)
	if n, _ := e.kv.do("EXISTS", keyEnv+"airsig/prod", keyVer+"airsig/prod", keyHist+"airsig/prod:MONGO_URI"); n.n != 0 {
		t.Fatalf("%d keys left after deleting the environment", n.n)
	}
	envs := e.must("GET", "/v1/envs", nil, 200)["envs"].([]any)
	if len(envs) != 0 {
		t.Fatalf("envs = %v", envs)
	}
}

func TestImportAndExport(t *testing.T) {
	e := newEnv(t)
	e.must("PUT", "/v1/envs/app/dev/secrets/KEEP", map[string]string{"value": "old"}, 200)
	out := e.must("POST", "/v1/envs/app/dev/import", map[string]any{"dotenv": "KEEP=new\nPORT=3000\nURL=\"http://x/?a=1#b\"\n"}, 200)
	if len(out["created"].([]any)) != 2 || len(out["skipped"].([]any)) != 1 {
		t.Fatalf("import = %v", out)
	}
	out = e.must("POST", "/v1/envs/app/dev/import", map[string]any{"dotenv": "KEEP=new\nPORT=3000\n", "overwrite": true}, 200)
	if len(out["updated"].([]any)) != 1 || len(out["unchanged"].([]any)) != 1 {
		t.Fatalf("overwrite import = %v", out)
	}
	if code, _ := e.admin("POST", "/v1/envs/app/dev/import", map[string]any{"dotenv": "bad line"}); code != 400 {
		t.Fatalf("bad dotenv = %d", code)
	}

	vals := e.must("GET", "/v1/envs/app/dev/export", nil, 200)["values"].(map[string]any)
	if vals["KEEP"] != "new" || vals["PORT"] != "3000" || vals["URL"] != "http://x/?a=1#b" {
		t.Fatalf("export = %v", vals)
	}
	_, _, text := e.req(e.tok, "GET", "/v1/envs/app/dev/export?format=dotenv", nil)
	back, err := ParseDotenv(text)
	if err != nil || back["URL"] != "http://x/?a=1#b" || len(back) != 3 {
		t.Fatalf("dotenv export %q -> %v %v", text, back, err)
	}
}

func TestReadTokens(t *testing.T) {
	e := newEnv(t)
	e.must("PUT", "/v1/envs/airsig/prod/secrets/A", map[string]string{"value": "prod-a"}, 200)
	e.must("PUT", "/v1/envs/airsig/dev/secrets/A", map[string]string{"value": "dev-a"}, 200)
	e.must("PUT", "/v1/envs/other/prod/secrets/A", map[string]string{"value": "other-a"}, 200)

	if code, _ := e.admin("POST", "/v1/tokens", map[string]any{"name": "x", "envs": []string{"nope"}}); code != 400 {
		t.Fatalf("bad scope = %d", code)
	}
	out := e.must("POST", "/v1/tokens", map[string]any{"name": "airsig-backend", "envs": []string{"airsig/prod"}}, 201)
	prodTok := out["secret"].(string)
	tokID := out["token"].(map[string]any)["id"].(string)
	wild := e.must("POST", "/v1/tokens", map[string]any{"name": "airsig-all", "envs": []string{"airsig/*"}}, 201)["secret"].(string)

	// What a read token can do
	code, vals, _ := e.req(prodTok, "GET", "/v1/envs/airsig/prod/export", nil)
	if code != 200 || vals["values"].(map[string]any)["A"] != "prod-a" {
		t.Fatalf("export with token = %d %v", code, vals)
	}
	if code, one, _ := e.req(prodTok, "GET", "/v1/envs/airsig/prod/secrets/A", nil); code != 200 || one["value"] != "prod-a" {
		t.Fatalf("get with token = %d %v", code, one)
	}
	if code, st, _ := e.req(prodTok, "GET", "/v1/status", nil); code != 200 || st["token"] == nil {
		t.Fatalf("status with token = %d %v", code, st)
	}
	// ... and cannot
	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/envs/airsig/dev/export"},
		{"GET", "/v1/envs/other/prod/secrets/A"},
		{"GET", "/v1/envs"},
		{"GET", "/v1/envs/airsig/prod/secrets"},
		{"PUT", "/v1/envs/airsig/prod/secrets/A"},
		{"GET", "/v1/tokens"},
		{"GET", "/v1/audit"},
	} {
		if code, _, _ := e.req(prodTok, c.method, c.path, map[string]string{"value": "x"}); code != 403 {
			t.Errorf("%s %s with a read token = %d, want 403", c.method, c.path, code)
		}
	}
	if code, _, _ := e.req(wild, "GET", "/v1/envs/airsig/dev/export", nil); code != 200 {
		t.Fatalf("project wildcard = %d", code)
	}
	if code, _, _ := e.req(wild, "GET", "/v1/envs/other/prod/export", nil); code != 403 {
		t.Fatalf("wildcard must stay in its project: %d", code)
	}
	tampered := prodTok[:len(prodTok)-1] + "0"
	if tampered == prodTok {
		tampered = prodTok[:len(prodTok)-1] + "1"
	}
	if code, _, _ := e.req(tampered, "GET", "/v1/envs/airsig/prod/export", nil); code != 401 {
		t.Fatalf("tampered token = %d", code)
	}

	// Listing shows when it was used, never the secret or its hash
	ts := e.must("GET", "/v1/tokens", nil, 200)["tokens"].([]any)
	t0 := ts[0].(map[string]any)
	if t0["hash"] != nil || t0["lastUsedMs"] == nil {
		t.Fatalf("token listing = %v", t0)
	}
	// The audit names the token
	entries := e.must("GET", "/v1/audit", nil, 200)["entries"].([]any)
	if entries[0].(map[string]any)["actor"] == "admin" {
		t.Fatalf("audit = %v", entries[:2])
	}

	e.must("DELETE", "/v1/tokens/"+tokID, nil, 200)
	if code, _, _ := e.req(prodTok, "GET", "/v1/envs/airsig/prod/export", nil); code != 401 {
		t.Fatalf("revoked token = %d", code)
	}
}

func TestActorHeaderAndLoopbackWithoutToken(t *testing.T) {
	addr := startKV(t)
	m, err := Start(Options{KVAddr: addr, APIListen: "127.0.0.1:0", Key: testKey})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	e := &env{t: t, api: "http://" + m.Addr()}
	req, _ := http.NewRequest("PUT", e.api+"/v1/envs/p/e/secrets/X", strings.NewReader(`{"value":"1"}`))
	req.Header.Set("X-Snugkv-Actor", "console")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("loopback without token: %v %v", resp, err)
	}
	resp.Body.Close()
	_, out, _ := e.req("", "GET", "/v1/audit", nil)
	if a := out["entries"].([]any)[0].(map[string]any)["actor"]; a != "admin:console" {
		t.Fatalf("actor = %v", a)
	}
}

func TestBrowserRequestsAndForeignHostsAreRefused(t *testing.T) {
	e := newEnv(t)
	req, _ := http.NewRequest("POST", e.api+"/v1/envs/p/e/import", strings.NewReader(`{"dotenv":"A=1"}`))
	req.Header.Set("Authorization", "Bearer "+e.tok)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("browser request = %d", resp.StatusCode)
	}

	// A token-less loopback API answers only to loopback host names (no DNS rebinding)
	addr := startKV(t)
	m, err := Start(Options{KVAddr: addr, APIListen: "127.0.0.1:0", Key: testKey})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for host, want := range map[string]int{"evil.example:80": 403, "localhost:1": 200, "127.0.0.1": 200, "[::1]:9": 200} {
		req, _ := http.NewRequest("GET", "http://"+m.Addr()+"/v1/status", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("Host %s = %d, want %d", host, resp.StatusCode, want)
		}
	}
}

func TestAdminTokensAndExpiry(t *testing.T) {
	e := newEnv(t)
	e.must("PUT", "/v1/envs/app/prod/secrets/A", map[string]string{"value": "1"}, 200)

	if code, _ := e.admin("POST", "/v1/tokens", map[string]any{"name": "x", "admin": true, "envs": []string{"app/prod"}}); code != 400 {
		t.Fatalf("admin token with envs = %d", code)
	}
	out := e.must("POST", "/v1/tokens", map[string]any{"name": "ci", "admin": true}, 201)
	ska := out["secret"].(string)
	if !strings.HasPrefix(ska, "ska_") || out["token"].(map[string]any)["admin"] != true {
		t.Fatalf("admin token = %v", out)
	}
	// It is an admin: creates a read token, changes secrets, reads the audit
	code, rt, _ := e.req(ska, "POST", "/v1/tokens", map[string]any{"name": "svc", "envs": []string{"app/prod"}, "ttlDays": 30})
	if code != 201 {
		t.Fatalf("admin token creating a read token = %d", code)
	}
	exp := rt["token"].(map[string]any)["expiresMs"].(float64)
	if d := exp - float64(time.Now().UnixMilli()); d < 29*24*3600*1000 || d > 31*24*3600*1000 {
		t.Fatalf("30-day expiry = %v ms from now", d)
	}
	req, _ := http.NewRequest("PUT", e.api+"/v1/envs/app/prod/secrets/B", strings.NewReader(`{"value":"2"}`))
	req.Header.Set("Authorization", "Bearer "+ska)
	req.Header.Set("X-Snugkv-Actor", "deploy")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("admin token PUT: %v %v", resp, err)
	}
	resp.Body.Close()
	entries := e.must("GET", "/v1/audit", nil, 200)["entries"].([]any)
	if a := entries[0].(map[string]any)["actor"]; a != "admin:ci/deploy" {
		t.Fatalf("audit actor = %v", a)
	}
	// A read token cannot make tokens; a read token presented as ska_ (or the reverse) does not work
	readTok := rt["secret"].(string)
	if code, _, _ := e.req(readTok, "POST", "/v1/tokens", map[string]any{"name": "y", "envs": []string{"app/prod"}}); code != 403 {
		t.Fatalf("read token creating tokens = %d", code)
	}
	if code, _, _ := e.req("ska_"+strings.TrimPrefix(readTok, "sks_"), "GET", "/v1/envs", nil); code != 401 {
		t.Fatalf("read token with the admin prefix = %d", code)
	}
	if code, _ := e.admin("POST", "/v1/tokens", map[string]any{"name": "z", "envs": []string{"app/prod"}, "ttlDays": 9999}); code != 400 {
		t.Fatalf("ttlDays out of range = %d", code)
	}

	// An expired token stops working
	id := rt["token"].(map[string]any)["id"].(string)
	raw, _ := e.kv.do("HGET", keyTokens, id)
	var tk Token
	json.Unmarshal([]byte(raw.str), &tk)
	tk.ExpiresMs = time.Now().UnixMilli() - 1000
	b, _ := json.Marshal(tk)
	e.kv.do("HSET", keyTokens, id, string(b))
	if code, _, _ := e.req(readTok, "GET", "/v1/envs/app/prod/export", nil); code != 401 {
		t.Fatalf("expired token = %d", code)
	}

	// Revoking the admin token
	adminID := out["token"].(map[string]any)["id"].(string)
	e.must("DELETE", "/v1/tokens/"+adminID, nil, 200)
	if code, _, _ := e.req(ska, "GET", "/v1/envs", nil); code != 401 {
		t.Fatalf("revoked admin token = %d", code)
	}
}

func TestKeyRotation(t *testing.T) {
	addr := startKV(t)
	const keyA, keyB = "first-key-0123456789", "second-key-0123456789"
	start := func(key, old string) (*Manager, error) {
		return Start(Options{KVAddr: addr, APIListen: "127.0.0.1:0", Key: key, OldKey: old})
	}
	m, err := start(keyA, "")
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, api: "http://" + m.Addr()}
	put := func(name, v string) {
		e.req("", "PUT", "/v1/envs/app/prod/secrets/"+name, map[string]string{"value": v})
	}
	put("A", "a1")
	put("A", "a2") // history has two versions
	put("B", "b1")
	e.req("", "DELETE", "/v1/envs/app/prod/secrets/B", nil) // history of a deleted secret
	m.Close()

	if _, err := start(keyB, ""); err == nil {
		t.Fatal("the new key alone must be refused before the rotation")
	}
	if _, err := start(keyB, "not-the-old-key-01234"); err == nil {
		t.Fatal("a wrong old key must be refused")
	}
	m, err = start(keyB, keyA)
	if err != nil {
		t.Fatalf("rotation: %v", err)
	}
	e.api = "http://" + m.Addr()
	_, one, _ := e.req("", "GET", "/v1/envs/app/prod/secrets/A", nil)
	if one["value"] != "a2" {
		t.Fatalf("after rotation A = %v", one)
	}
	m.Close()

	// From now on the new key alone is enough, and the old one opens nothing
	m, err = start(keyB, "")
	if err != nil {
		t.Fatalf("new key after the rotation: %v", err)
	}
	e.api = "http://" + m.Addr()
	_, one, _ = e.req("", "GET", "/v1/envs/app/prod/secrets/A", nil)
	if one["value"] != "a2" {
		t.Fatalf("A with the new key = %v", one)
	}
	_, vs, _ := e.req("", "GET", "/v1/envs/app/prod/secrets/A/versions", nil)
	for _, v := range vs["versions"].([]any) {
		if v.(map[string]any)["hint"] == nil || v.(map[string]any)["hint"] == "" {
			t.Fatalf("history entry not readable with the new key: %v", v)
		}
	}
	if code, out, _ := e.req("", "POST", "/v1/envs/app/prod/secrets/B/rollback", map[string]int{"version": 1}); code != 200 {
		t.Fatalf("restoring a deleted secret's old version after rotation = %d %v", code, out)
	}
	m.Close()
	if _, err := start(keyA, ""); err == nil {
		t.Fatal("the old key must no longer open the data")
	}
}

func TestRotationThatStoppedHalfwayContinues(t *testing.T) {
	addr := startKV(t)
	const keyA, keyB = "first-key-0123456789", "second-key-0123456789"
	m, _ := Start(Options{KVAddr: addr, APIListen: "127.0.0.1:0", Key: keyA})
	e := &env{t: t, api: "http://" + m.Addr()}
	e.req("", "PUT", "/v1/envs/app/prod/secrets/A", map[string]string{"value": "a"})
	e.req("", "PUT", "/v1/envs/app/prod/secrets/B", map[string]string{"value": "b"})
	m.Close()
	// Simulate a rotation that re-sealed A and stopped: A under B's key, B and the check value still under A's
	kv := newKVClient(addr, "", "", 1, 5*time.Second)
	defer kv.close()
	sa, _ := newSealer(keyA)
	sb, _ := newSealer(keyB)
	raw, _ := kv.do("HGET", keyEnv+"app/prod", "A")
	var r record
	json.Unmarshal([]byte(raw.str), &r)
	plain, _ := sa.open(r.Value, bind("app/prod", "A"))
	r.Value, _ = sb.seal(plain, bind("app/prod", "A"))
	b, _ := json.Marshal(r)
	kv.do("HSET", keyEnv+"app/prod", "A", string(b))

	m, err := Start(Options{KVAddr: addr, APIListen: "127.0.0.1:0", Key: keyB, OldKey: keyA})
	if err != nil {
		t.Fatalf("continuing the rotation: %v", err)
	}
	m.Close()
	m, err = Start(Options{KVAddr: addr, APIListen: "127.0.0.1:0", Key: keyB})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	e.api = "http://" + m.Addr()
	_, out, _ := e.req("", "GET", "/v1/envs/app/prod/export", nil)
	vals := out["values"].(map[string]any)
	if vals["A"] != "a" || vals["B"] != "b" {
		t.Fatalf("after a resumed rotation: %v", vals)
	}
}
