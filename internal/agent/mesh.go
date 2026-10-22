package agent

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The mesh: a WireGuard network between the servers of a region, so SnugKV,
// Swarm and the agents can talk over private addresses only.
//
// `snug-agent mesh` runs as root next to the reporting agent. It keeps the
// interface (snug0) up with this server's mesh address, publishes this
// server's public key and endpoint to snug:agent:mesh:<region>, and makes the
// interface's peers match what the other servers published. The private key
// never leaves the server. Peers are only added or changed after a
// successful read, so SnugKV being away never tears the mesh down, and the
// interface stays up when the agent stops.

// MeshRecord is what each server publishes about itself.
type MeshRecord struct {
	Server     string `json:"server"`
	PublicKey  string `json:"publicKey"`
	IP         string `json:"ip"`       // mesh address with prefix, 10.88.1.5/24
	Endpoint   string `json:"endpoint"` // public address WireGuard listens on, 203.0.113.7:51820
	UpdatedMs  int64  `json:"updatedMs"`
	PeersTotal int    `json:"peersTotal"`
	PeersUp    int    `json:"peersUp"` // peers with a handshake in the last 3 minutes
	Error      string `json:"error,omitempty"`
	Agent      string `json:"agent"`
}

// Runner runs a command and returns its output (combined stdout and stderr).
type Runner func(name string, args ...string) (string, error)

func execRunner(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// MeshOptions configure `snug-agent mesh`.
type MeshOptions struct {
	URL, CAFile    string
	Region, Server string
	IP             string        // this server's mesh address with prefix (from the console at join)
	KeyFile        string        // WireGuard private key, created when missing; default /etc/snug-agent/wg.key
	Iface          string        // default snug0
	Port           int           // default 51820
	Endpoint       string        // public address peers use; default: this server's address towards the internet
	Interval       time.Duration // default 10s
	Run            Runner
	Logf           func(string, ...any)
}

func (o *MeshOptions) normalize() error {
	o.Region, o.Server = strings.ToLower(o.Region), strings.ToLower(o.Server)
	if !nameRe.MatchString(o.Region) || !nameRe.MatchString(o.Server) {
		return errors.New("region and server names use a-z, 0-9, . _ - (up to 63 characters)")
	}
	if _, err := netip.ParsePrefix(o.IP); err != nil {
		return fmt.Errorf("mesh address %q: want an address with prefix, like 10.88.1.5/24", o.IP)
	}
	if o.KeyFile == "" {
		o.KeyFile = "/etc/snug-agent/wg.key"
	}
	if o.Iface == "" {
		o.Iface = "snug0"
	}
	if o.Port == 0 {
		o.Port = 51820
	}
	if o.Interval == 0 {
		o.Interval = 10 * time.Second
	}
	if o.Run == nil {
		o.Run = execRunner
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	return nil
}

// EnsureKey returns the public key of the WireGuard private key in file,
// creating the key (0600) when the file does not exist.
func EnsureKey(file string) (string, error) {
	raw, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		k, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			return "", err
		}
		if err := writeFileAtomic(file, []byte(base64.StdEncoding.EncodeToString(k.Bytes())+"\n"), 0o600); err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
	}
	if err != nil {
		return "", err
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(b) != 32 {
		return "", fmt.Errorf("%s is not a WireGuard private key", file)
	}
	k, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

// DetectEndpoint is this server's public address with the WireGuard port, or "".
func DetectEndpoint(port int) string {
	if ip := publicEndpointIP(""); ip != "" {
		return net.JoinHostPort(ip, strconv.Itoa(port))
	}
	return ""
}

// publicEndpointIP is the address this server uses towards the internet
// (no packet is sent). On bare metal with a public address, that address.
func publicEndpointIP(fallbackHost string) string {
	targets := []string{"1.1.1.1:53", "[2606:4700:4700::1111]:53"}
	if fallbackHost != "" {
		targets = append(targets, net.JoinHostPort(fallbackHost, "9"))
	}
	for _, target := range targets {
		c, err := net.Dial("udp", target)
		if err != nil {
			continue
		}
		ip := c.LocalAddr().(*net.UDPAddr).IP
		c.Close()
		if !ip.IsLoopback() && !ip.IsUnspecified() {
			return ip.String()
		}
	}
	return ""
}

type wgPeer struct {
	endpoint   string
	allowed    string
	handshakeS int64
}

// parseDump reads `wg show <iface> dump`: the first line is the interface,
// then one line per peer.
func parseDump(out string) map[string]wgPeer {
	peers := map[string]wgPeer{}
	for i, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\t")
		if i == 0 || len(f) < 8 {
			continue
		}
		hs, _ := strconv.ParseInt(f[4], 10, 64)
		ep := f[2]
		if ep == "(none)" {
			ep = ""
		}
		allowed := f[3]
		if allowed == "(none)" {
			allowed = ""
		}
		peers[f[0]] = wgPeer{endpoint: ep, allowed: allowed, handshakeS: hs}
	}
	return peers
}

// validPeer checks a record another server wrote before it goes into wg.
func validPeer(r MeshRecord, subnet netip.Prefix) (key, allowed, endpoint string, ok bool) {
	b, err := base64.StdEncoding.DecodeString(r.PublicKey)
	if err != nil || len(b) != 32 {
		return "", "", "", false
	}
	p, err := netip.ParsePrefix(r.IP)
	if err != nil || !subnet.Contains(p.Addr()) {
		return "", "", "", false
	}
	ap, err := netip.ParseAddrPort(r.Endpoint)
	if err != nil || ap.Port() == 0 {
		return "", "", "", false
	}
	host := netip.PrefixFrom(p.Addr(), p.Addr().BitLen())
	return r.PublicKey, host.String(), ap.String(), true
}

func mustPrefix(s string) netip.Prefix { return netip.MustParsePrefix(s) }

// hostPrefix turns 10.88.1.5/24 into 10.88.1.5/32.
func hostPrefix(s string) string {
	a := mustPrefix(s).Addr()
	return netip.PrefixFrom(a, a.BitLen()).String()
}

type mesh struct {
	o       MeshOptions
	kv      *kvConn
	pub     string
	subnet  netip.Prefix
	self    string // own mesh address as a host prefix, 10.88.1.5/32
	ready   bool
	lastErr string
}

// RunMesh keeps this server's WireGuard interface and peers in step with the
// region until ctx ends.
func RunMesh(ctx context.Context, o MeshOptions) error {
	if err := o.normalize(); err != nil {
		return err
	}
	pub, err := EnsureKey(o.KeyFile)
	if err != nil {
		return fmt.Errorf("WireGuard key: %w", err)
	}
	kv, err := parseKVURL(o.URL, o.CAFile)
	if err != nil {
		return err
	}
	defer kv.close()
	if o.Endpoint == "" {
		u, _ := url.Parse(o.URL)
		if ip := publicEndpointIP(u.Hostname()); ip != "" {
			o.Endpoint = net.JoinHostPort(ip, strconv.Itoa(o.Port))
		}
	} else if _, _, err := net.SplitHostPort(o.Endpoint); err != nil {
		o.Endpoint = net.JoinHostPort(o.Endpoint, strconv.Itoa(o.Port))
	}
	if _, err := netip.ParseAddrPort(o.Endpoint); err != nil {
		return fmt.Errorf("cannot tell this server's public address (%q); set SNUG_AGENT_ENDPOINT", o.Endpoint)
	}
	m := &mesh{o: o, kv: kv, pub: pub, subnet: mustPrefix(o.IP).Masked(), self: hostPrefix(o.IP)}
	o.Logf("mesh %s on %s as %s (%s), endpoint %s", o.Region, o.Iface, o.IP, pub, o.Endpoint)
	m.bootstrap(filepath.Join(filepath.Dir(o.KeyFile), MeshBootstrapFile))
	t := time.NewTicker(o.Interval)
	defer t.Stop()
	for {
		m.step()
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// step does one round: interface, peers, own record.
func (m *mesh) step() {
	rec := MeshRecord{Server: m.o.Server, PublicKey: m.pub, IP: m.o.IP, Endpoint: m.o.Endpoint, Agent: Version}
	err := m.ensureIface()
	if err == nil {
		rec.PeersTotal, rec.PeersUp, err = m.syncPeers()
	}
	if err != nil {
		rec.Error = err.Error()
		if rec.Error != m.lastErr {
			m.o.Logf("mesh: %v", err)
		}
	} else if m.lastErr != "" {
		m.o.Logf("mesh: working again")
	}
	m.lastErr = rec.Error
	rec.UpdatedMs = time.Now().UnixMilli()
	b, _ := json.Marshal(rec)
	if _, err := m.kv.do("HSET", KeyMesh+m.o.Region, m.o.Server, string(b)); err != nil && m.lastErr == "" {
		m.o.Logf("mesh: cannot publish to SnugKV: %v", err)
		m.lastErr = err.Error()
	}
}

// bootstrap adds the peers the console gave at join (nothing is removed), so
// SnugKV can be reached over the mesh before the first sync.
func (m *mesh) bootstrap(file string) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return
	}
	var peers []MeshRecord
	if json.Unmarshal(raw, &peers) != nil || m.ensureIface() != nil {
		return
	}
	n := 0
	for _, r := range peers {
		key, allowed, endpoint, ok := validPeer(r, m.subnet)
		if !ok || key == m.pub || allowed == m.self {
			continue
		}
		if _, err := m.o.Run("wg", "set", m.o.Iface, "peer", key, "endpoint", endpoint, "allowed-ips", allowed, "persistent-keepalive", "25"); err == nil {
			n++
		}
	}
	if n > 0 {
		m.o.Logf("mesh: started with %d peer(s) from the join", n)
	}
}

func (m *mesh) ensureIface() error {
	run := m.o.Run
	if _, err := run("ip", "link", "show", "dev", m.o.Iface); err != nil {
		m.ready = false
		if _, err := run("ip", "link", "add", "dev", m.o.Iface, "type", "wireguard"); err != nil {
			return fmt.Errorf("creating %s (is WireGuard installed?): %w", m.o.Iface, err)
		}
	}
	if m.ready {
		return nil
	}
	steps := [][]string{
		{"wg", "set", m.o.Iface, "listen-port", strconv.Itoa(m.o.Port), "private-key", m.o.KeyFile},
		{"ip", "address", "replace", m.o.IP, "dev", m.o.Iface},
		{"ip", "link", "set", "mtu", "1420", "up", "dev", m.o.Iface},
	}
	for _, s := range steps {
		if _, err := run(s[0], s[1:]...); err != nil {
			return err
		}
	}
	m.ready = true
	return nil
}

// syncPeers makes the interface's peers match the region's records.
func (m *mesh) syncPeers() (total, up int, err error) {
	flat, err := m.kv.doList("HGETALL", KeyMesh+m.o.Region)
	if err != nil {
		return 0, 0, fmt.Errorf("reading the region's servers: %w", err)
	}
	want := map[string][2]string{} // key -> allowed, endpoint
	usedIP := map[string]string{}
	names := make([]string, 0, len(flat)/2)
	recs := map[string]MeshRecord{}
	for i := 0; i+1 < len(flat); i += 2 {
		var r MeshRecord
		if json.Unmarshal([]byte(flat[i+1]), &r) == nil {
			names = append(names, flat[i])
			recs[flat[i]] = r
		}
	}
	sort.Strings(names) // the same winner everywhere when two servers claim an address
	for _, name := range names {
		r := recs[name]
		if name == m.o.Server || r.PublicKey == m.pub {
			continue
		}
		key, allowed, endpoint, ok := validPeer(r, m.subnet)
		if !ok || allowed == m.self {
			continue
		}
		if other, taken := usedIP[allowed]; taken {
			m.o.Logf("mesh: %s and %s both use %s; using %s", other, name, allowed, other)
			continue
		}
		usedIP[allowed] = name
		want[key] = [2]string{allowed, endpoint}
	}
	out, err := m.o.Run("wg", "show", m.o.Iface, "dump")
	if err != nil {
		m.ready = false
		return 0, 0, err
	}
	have := parseDump(out)
	for key, w := range want {
		h, ok := have[key]
		if ok && h.allowed == w[0] && h.endpoint == w[1] {
			continue
		}
		if _, err := m.o.Run("wg", "set", m.o.Iface, "peer", key, "endpoint", w[1], "allowed-ips", w[0], "persistent-keepalive", "25"); err != nil {
			return 0, 0, err
		}
	}
	for key := range have {
		if _, ok := want[key]; !ok {
			if _, err := m.o.Run("wg", "set", m.o.Iface, "peer", key, "remove"); err != nil {
				return 0, 0, err
			}
		}
	}
	now := time.Now().Unix()
	for key := range want {
		if h, ok := have[key]; ok && h.handshakeS > 0 && now-h.handshakeS < 180 {
			up++
		}
	}
	return len(want), up, nil
}
