package agent

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"snugkv/internal/config"
	"snugkv/internal/engine"
	"snugkv/internal/server"
)

func TestEnsureKey(t *testing.T) {
	dir := t.TempDir()
	// The example key from wg(8)
	f := filepath.Join(dir, "known.key")
	must(t, os.WriteFile(f, []byte("yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=\n"), 0o600))
	pub, err := EnsureKey(f)
	must(t, err)
	if pub != "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw=" {
		t.Fatalf("public key %s", pub)
	}
	nf := filepath.Join(dir, "sub", "wg.key")
	p1, err := EnsureKey(nf)
	must(t, err)
	p2, err := EnsureKey(nf)
	must(t, err)
	if p1 != p2 || len(p1) != 44 {
		t.Fatalf("new key not kept: %s %s", p1, p2)
	}
	if st, _ := os.Stat(nf); st.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v", st.Mode())
	}
	must(t, os.WriteFile(f, []byte("nope"), 0o600))
	if _, err := EnsureKey(f); err == nil {
		t.Fatal("a bad key must be refused")
	}
}

// fakeWG stands in for `ip` and `wg` on one server.
type fakeWG struct {
	mu        sync.Mutex
	exists    bool
	up        bool
	addr      string
	port      string
	peers     map[string][2]string // key -> allowed, endpoint
	handshake map[string]int64
	calls     []string
}

func newFakeWG() *fakeWG {
	return &fakeWG{peers: map[string][2]string{}, handshake: map[string]int64{}}
}

func (f *fakeWG) run(name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, cmd)
	switch {
	case strings.HasPrefix(cmd, "ip link show"):
		if !f.exists {
			return "", fmt.Errorf("Device does not exist")
		}
	case strings.HasPrefix(cmd, "ip link add"):
		f.exists = true
	case strings.HasPrefix(cmd, "ip address replace"):
		f.addr = args[2]
	case strings.HasPrefix(cmd, "ip link set"):
		f.up = true
	case name == "wg" && args[0] == "show":
		var b strings.Builder
		b.WriteString("priv\tpub\t" + f.port + "\toff\n")
		keys := make([]string, 0, len(f.peers))
		for k := range f.peers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := f.peers[k]
			fmt.Fprintf(&b, "%s\t(none)\t%s\t%s\t%d\t0\t0\t25\n", k, p[1], p[0], f.handshake[k])
		}
		return b.String(), nil
	case name == "wg" && args[0] == "set" && len(args) > 3 && args[2] == "peer":
		key := args[3]
		if args[4] == "remove" {
			delete(f.peers, key)
		} else {
			f.peers[key] = [2]string{args[7], args[5]}
		}
	case name == "wg" && args[0] == "set":
		f.port = args[3]
	default:
		return "", fmt.Errorf("unexpected %s", cmd)
	}
	return "", nil
}

func (f *fakeWG) snapshot() map[string][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string][2]string{}
	for k, v := range f.peers {
		out[k] = v
	}
	return out
}

func testKV(t *testing.T) string {
	c := config.Default()
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	c.ListenAddr = l.Addr().String()
	l.Close()
	l, _ = net.Listen("tcp", "127.0.0.1:0")
	c.AdminAddr = l.Addr().String()
	l.Close()
	srv, err := server.ListenWithConfig(c, engine.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return "redis://" + c.ListenAddr
}

func newTestMesh(t *testing.T, url, server, ip, endpoint string, wg *fakeWG) *mesh {
	o := MeshOptions{URL: url, Region: "eu-1", Server: server, IP: ip, Endpoint: endpoint, KeyFile: filepath.Join(t.TempDir(), "wg.key"), Run: wg.run}
	must(t, o.normalize())
	pub, err := EnsureKey(o.KeyFile)
	must(t, err)
	kv, err := parseKVURL(url, "")
	must(t, err)
	t.Cleanup(kv.close)
	m := &mesh{o: o, kv: kv, pub: pub}
	m.subnet = mustPrefix(ip).Masked()
	m.self = hostPrefix(ip)
	return m
}

func TestMeshSync(t *testing.T) {
	url := testKV(t)
	wg1, wg2 := newFakeWG(), newFakeWG()
	m1 := newTestMesh(t, url, "node-1", "10.88.1.1/24", "203.0.113.1:51820", wg1)
	m2 := newTestMesh(t, url, "node-2", "10.88.1.2/24", "203.0.113.2:51820", wg2)

	m1.step() // node-1 alone: interface up, no peers
	if !wg1.exists || !wg1.up || wg1.addr != "10.88.1.1/24" || wg1.port != "51820" || len(wg1.peers) != 0 {
		t.Fatalf("interface: %+v", wg1)
	}
	m2.step()
	m1.step()
	if p := wg1.snapshot()[m2.pub]; p != [2]string{"10.88.1.2/32", "203.0.113.2:51820"} {
		t.Fatalf("node-1 peers: %v", wg1.snapshot())
	}
	if p := wg2.snapshot()[m1.pub]; p != [2]string{"10.88.1.1/32", "203.0.113.1:51820"} {
		t.Fatalf("node-2 peers: %v", wg2.snapshot())
	}

	// Bad records from SnugKV never reach wg; a second claim on an address loses
	kv := m1.kv
	bad := []MeshRecord{
		{Server: "evil-1", PublicKey: "not-a-key", IP: "10.88.1.9/24", Endpoint: "198.51.100.1:51820"},
		{Server: "evil-2", PublicKey: m2.pub[:43] + "A", IP: "10.99.0.1/24", Endpoint: "198.51.100.1:51820"}, // outside the subnet
		{Server: "evil-3", PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", IP: "10.88.1.10/24", Endpoint: "nohost"},
		{Server: "node-3", PublicKey: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBA=", IP: "10.88.1.2/24", Endpoint: "203.0.113.3:51820"}, // node-2's address
		{Server: "node-0", PublicKey: "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCA=", IP: "10.88.1.1/24", Endpoint: "203.0.113.4:51820"}, // node-1's own address
	}
	for _, r := range bad {
		b, _ := json.Marshal(r)
		_, err := kv.do("HSET", KeyMesh+"eu-1", r.Server, string(b))
		must(t, err)
	}
	m1.step()
	if got := wg1.snapshot(); len(got) != 1 || got[m2.pub][0] != "10.88.1.2/32" {
		t.Fatalf("node-1 peers after bad records: %v", got)
	}
	for _, r := range bad {
		kv.do("HDEL", KeyMesh+"eu-1", r.Server)
	}

	// A peer that moved gets its new endpoint; one that is gone is removed
	wg1.mu.Lock()
	wg1.handshake[m2.pub] = time.Now().Unix()
	wg1.mu.Unlock()
	m2.o.Endpoint = "203.0.113.22:51820"
	m2.step()
	m1.step()
	if got := wg1.snapshot()[m2.pub][1]; got != "203.0.113.22:51820" {
		t.Fatalf("endpoint not updated: %s", got)
	}
	raw, _ := kv.do("HGET", KeyMesh+"eu-1", "node-1")
	var rec MeshRecord
	must(t, json.Unmarshal([]byte(raw), &rec))
	if rec.PeersTotal != 1 || rec.PeersUp != 1 || rec.Error != "" || rec.PublicKey != m1.pub || rec.IP != "10.88.1.1/24" {
		t.Fatalf("record: %+v", rec)
	}
	kv.do("HDEL", KeyMesh+"eu-1", "node-2")
	m1.step()
	if got := wg1.snapshot(); len(got) != 0 {
		t.Fatalf("removed server still a peer: %v", got)
	}

	// SnugKV away: nothing is removed
	m2.step()
	m1.step()
	m1.kv.close()
	m1.kv.addr = "127.0.0.1:1"
	m1.step()
	if got := wg1.snapshot(); len(got) != 1 {
		t.Fatalf("peers changed while SnugKV was away: %v", got)
	}

	// The interface disappeared (reboot without the unit): created again
	wg2.mu.Lock()
	wg2.exists, wg2.up = false, false
	wg2.mu.Unlock()
	m2.step()
	if !wg2.exists || !wg2.up {
		t.Fatal("interface not recreated")
	}
}

func TestParseDump(t *testing.T) {
	out := "cHJpdg==\tcHVi\t51820\toff\n" +
		"KEY1=\t(none)\t203.0.113.2:51820\t10.88.1.2/32\t1700000000\t10\t20\t25\n" +
		"KEY2=\t(none)\t(none)\t(none)\t0\t0\t0\toff\n"
	p := parseDump(out)
	if p["KEY1="].endpoint != "203.0.113.2:51820" || p["KEY1="].allowed != "10.88.1.2/32" || p["KEY1="].handshakeS != 1700000000 {
		t.Fatalf("%+v", p)
	}
	if p["KEY2="].endpoint != "" || p["KEY2="].allowed != "" {
		t.Fatalf("%+v", p)
	}
}

func TestMeshOptions(t *testing.T) {
	o := MeshOptions{Region: "eu-1", Server: "a", IP: "10.88.1.1"}
	if err := o.normalize(); err == nil {
		t.Fatal("an address without prefix must be refused")
	}
}

func TestMeshBootstrapFromJoin(t *testing.T) {
	url := testKV(t)
	wg := newFakeWG()
	m := newTestMesh(t, url, "node-9", "10.88.1.9/24", "203.0.113.9:51820", wg)
	peers := []MeshRecord{
		{Server: "kv-1", PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", IP: "10.88.1.1/24", Endpoint: "203.0.113.1:51820"},
		{Server: "self", PublicKey: m.pub, IP: "10.88.1.9/24", Endpoint: "203.0.113.9:51820"},
		{Server: "bad", PublicKey: "x", IP: "10.88.1.3/24", Endpoint: "203.0.113.3:51820"},
	}
	b, _ := json.Marshal(peers)
	f := filepath.Join(t.TempDir(), MeshBootstrapFile)
	must(t, os.WriteFile(f, b, 0o600))
	m.bootstrap(f)
	got := wg.snapshot()
	if len(got) != 1 || got["AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="] != [2]string{"10.88.1.1/32", "203.0.113.1:51820"} {
		t.Fatalf("bootstrap peers: %v", got)
	}
}
