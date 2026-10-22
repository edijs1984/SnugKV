package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func signed(t *testing.T, priv ed25519.PrivateKey, c Command) (string, string) {
	t.Helper()
	b, _ := json.Marshal(c)
	return string(b), base64.StdEncoding.EncodeToString(ed25519.Sign(priv, b))
}

func TestVerifyCommand(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now()
	ok := Command{ID: "c1", Region: "eu-1", Server: "a", Kind: "ping", IssuedMs: now.UnixMilli(), ExpiresMs: now.Add(time.Minute).UnixMilli()}
	p, s := signed(t, priv, ok)
	if _, err := VerifyCommand(pub, p, s, "eu-1", "a", now); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func() (string, string, string){
		"bad signature": func() (string, string, string) {
			return strings.Replace(p, "ping", "pong", 1), s, "a"
		},
		"for eu-1/a": func() (string, string, string) { return p, s, "b" },
		"expired": func() (string, string, string) {
			c := ok
			c.ExpiresMs = now.Add(-time.Second).UnixMilli()
			p, s := signed(t, priv, c)
			return p, s, "a"
		},
		"expired ": func() (string, string, string) { // lives too long
			c := ok
			c.ExpiresMs = now.Add(time.Hour).UnixMilli()
			p, s := signed(t, priv, c)
			return p, s, "a"
		},
		"future": func() (string, string, string) {
			c := ok
			c.IssuedMs, c.ExpiresMs = now.Add(10*time.Minute).UnixMilli(), now.Add(12*time.Minute).UnixMilli()
			p, s := signed(t, priv, c)
			return p, s, "a"
		},
	}
	for want, mk := range cases {
		p, s, server := mk()
		if _, err := VerifyCommand(pub, p, s, "eu-1", server, now); err == nil || !strings.Contains(err.Error(), strings.TrimSpace(want)) {
			t.Errorf("%s: got %v", want, err)
		}
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := VerifyCommand(other, p, s, "eu-1", "a", now); err == nil {
		t.Error("another key must not verify")
	}
	if _, err := ParseCommandKey("abc"); err == nil {
		t.Error("short key accepted")
	}
}

// swarmDocker is a fake engine that can init and join a swarm.
type swarmDocker struct {
	mu     sync.Mutex
	state  string
	joined map[string]any
	inited map[string]any
}

func (d *swarmDocker) socket(t *testing.T) string {
	sock := filepath.Join(t.TempDir(), "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ServerVersion": "27", "Swarm": map[string]any{"LocalNodeState": d.state, "NodeID": "node-x", "ControlAvailable": d.inited != nil}})
	})
	mux.HandleFunc("/containers/json", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("[]")) })
	mux.HandleFunc("/swarm", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"JoinTokens":{"Worker":"SWMTKN-w","Manager":"SWMTKN-m"}}`))
	})
	mux.HandleFunc("/swarm/init", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		json.NewDecoder(r.Body).Decode(&d.inited)
		d.state = "active"
		w.Write([]byte(`"node-x"`))
	})
	mux.HandleFunc("/swarm/join", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.state == "active" {
			w.WriteHeader(503)
			w.Write([]byte(`{"message":"This node is already part of a swarm."}`))
			return
		}
		json.NewDecoder(r.Body).Decode(&d.joined)
		d.state = "active"
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return sock
}

func TestCommandsEndToEnd(t *testing.T) {
	url := testKV(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	dock := &swarmDocker{state: "inactive"}
	state := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := Options{URL: url, Region: "eu-1", Server: "kv-1", Interval: time.Second, Paths: fakeHost(t), Docker: dock.socket(t),
		CommandKey: base64.StdEncoding.EncodeToString(pub), StateDir: state, MeshIP: "10.88.1.1/24"}
	go Run(ctx, opts)

	kv, _ := parseKVURL(url, "")
	defer kv.close()
	stream := KeyCommands + "eu-1:kv-1"
	send := func(c Command, sigKey ed25519.PrivateKey) {
		now := time.Now()
		c.Region, c.IssuedMs, c.ExpiresMs, c.By = "eu-1", now.UnixMilli(), now.Add(time.Minute).UnixMilli(), "anna"
		if c.Server == "" {
			c.Server = "kv-1"
		}
		p, s := signed(t, sigKey, c)
		if _, err := kv.do("XADD", stream, "*", "c", p, "s", s); err != nil {
			t.Fatal(err)
		}
	}
	result := func(id string) CommandResult {
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			v, _ := kv.doAny("XRANGE", KeyResults+"eu-1:kv-1", "-", "+")
			for _, e := range func() []any { l, _ := v.([]any); return l }() {
				ep := e.([]any)
				f := ep[1].([]any)
				var r CommandResult
				json.Unmarshal([]byte(f[1].(string)), &r)
				if r.ID == id {
					return r
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("no result for %s", id)
		return CommandResult{}
	}
	// The card says commands are on
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, _ := kv.do("GET", KeyCard+"eu-1:kv-1")
		if strings.Contains(raw, `"commands":true`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("card without commands:true")
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond) // the reader has its start position

	send(Command{ID: "p1", Kind: "ping"}, priv)
	if r := result("p1"); !r.OK || r.Output != "pong" {
		t.Fatalf("ping: %+v", r)
	}
	// Forged (another key), for another server, replayed: not run
	_, forger, _ := ed25519.GenerateKey(rand.Reader)
	send(Command{ID: "f1", Kind: "swarm.leave"}, forger)
	if r := result("f1"); r.OK || !strings.Contains(r.Output, "bad signature") {
		t.Fatalf("forged: %+v", r)
	}
	send(Command{ID: "s1", Kind: "swarm.init"}, priv)
	r := result("s1")
	tokens, _ := json.Marshal(r.Data)
	if !r.OK || !strings.Contains(string(tokens), "SWMTKN-m") || !strings.Contains(string(tokens), `"advertiseAddr":"10.88.1.1"`) {
		t.Fatalf("swarm.init: %+v", r)
	}
	if dock.inited["AdvertiseAddr"] != "10.88.1.1" || dock.inited["ListenAddr"] != "10.88.1.1:2377" {
		t.Fatalf("init request: %v", dock.inited)
	}
	send(Command{ID: "j1", Kind: "swarm.join", Args: json.RawMessage(`{"managers":["10.88.1.2"],"token":"SWMTKN-w"}`)}, priv)
	if r := result("j1"); r.OK || !strings.Contains(r.Output, "already in a swarm") {
		t.Fatalf("join while in a swarm: %+v", r)
	}
	send(Command{ID: "x1", Kind: "rm -rf /"}, priv)
	if r := result("x1"); r.OK || !strings.Contains(r.Output, "unknown command") {
		t.Fatalf("unknown: %+v", r)
	}
	// The same signed command again (a replay) is ignored
	now := time.Now()
	c := Command{ID: "p1", Region: "eu-1", Server: "kv-1", Kind: "ping", IssuedMs: now.UnixMilli(), ExpiresMs: now.Add(time.Minute).UnixMilli()}
	p, s := signed(t, priv, c)
	kv.do("XADD", stream, "*", "c", p, "s", s)
	send(Command{ID: "p2", Kind: "ping"}, priv)
	result("p2")
	v, _ := kv.doAny("XRANGE", KeyResults+"eu-1:kv-1", "-", "+")
	n := 0
	for _, e := range v.([]any) {
		if strings.Contains(e.([]any)[1].([]any)[1].(string), `"id":"p1"`) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("p1 ran %d times", n)
	}
	// The position survives a restart
	cancel()
	time.Sleep(200 * time.Millisecond)
	cr, _ := newCommandRunner(opts, nil)
	if last := cr.lastID(); last == "" {
		t.Fatal("last command id not saved")
	}
}

func TestSwarmJoinCommand(t *testing.T) {
	dock := &swarmDocker{state: "inactive"}
	dc := newDockerClient(dock.socket(t))
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	cr, err := newCommandRunner(Options{Region: "eu-1", Server: "app-1", CommandKey: base64.StdEncoding.EncodeToString(pub), MeshIP: "10.88.1.5/24"}, dc)
	must(t, err)
	_, out, err := cr.run(context.Background(), Command{Kind: "swarm.join", Args: json.RawMessage(`{"managers":["10.88.1.1","10.88.1.2:2377"],"token":"SWMTKN-w"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "10.88.1.5") || dock.joined["JoinToken"] != "SWMTKN-w" || dock.joined["AdvertiseAddr"] != "10.88.1.5" {
		t.Fatalf("join: %s %v", out, dock.joined)
	}
	if m := dock.joined["RemoteAddrs"].([]any); m[0] != "10.88.1.1:2377" || m[1] != "10.88.1.2:2377" {
		t.Fatalf("managers %v", m)
	}
	if _, _, err := cr.run(context.Background(), Command{Kind: "swarm.join", Args: json.RawMessage(`{"managers":["evil host"],"token":"x"}`)}); err == nil {
		t.Fatal("a bad manager address must be refused")
	}
	noMesh, _ := newCommandRunner(Options{Region: "eu-1", Server: "x", CommandKey: base64.StdEncoding.EncodeToString(pub)}, dc)
	if _, _, err := noMesh.run(context.Background(), Command{Kind: "swarm.init"}); err == nil || !strings.Contains(err.Error(), "no mesh address") {
		t.Fatalf("init without mesh: %v", err)
	}
}
