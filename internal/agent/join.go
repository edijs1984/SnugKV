package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// JoinRequest is what a new server sends to the console with its one-time
// join token (POST <panel>/api/join/redeem).
type JoinRequest struct {
	Token     string `json:"token"`
	Name      string `json:"name,omitempty"` // wanted server name; the console may change it
	Hostname  string `json:"hostname"`
	MachineID string `json:"machineId"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Agent     string `json:"agent"`
	// For the region's WireGuard mesh: this server's public key (the private
	// key stays on the server) and the address other servers reach it on
	WGPublicKey string `json:"wgPublicKey,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
}

// JoinConfig is the console's answer: where and as what this server reports.
type JoinConfig struct {
	Region    string       `json:"region"`
	Server    string       `json:"server"`
	URL       string       `json:"url"`                 // SnugKV address with the agent's credentials
	CA        string       `json:"ca,omitempty"`        // PEM, for rediss:// with a private CA
	MeshIP    string       `json:"meshIp,omitempty"`    // this server's address in the region's WireGuard mesh, 10.88.1.5/24
	MeshPeers []MeshRecord `json:"meshPeers,omitempty"` // the region's servers at join time, to reach them before SnugKV
}

// Join redeems a join token at the console and returns this server's agent
// settings. The token works once.
//
// With mesh set (WireGuard public key and endpoint), the console also gives
// this server a mesh address and the current peers.
func Join(ctx context.Context, panel, token, name string, p Paths, mesh *JoinMesh) (JoinConfig, error) {
	p = p.withDefaults()
	c := newCollector(p)
	inv := c.inventory()
	host := inv.Hostname
	if host == "" {
		host, _ = os.Hostname()
	}
	req := JoinRequest{
		Token:     strings.TrimSpace(token),
		Name:      name,
		Hostname:  host,
		MachineID: readTrim(filepath.Join(p.Root, "etc/machine-id")),
		OS:        inv.OS,
		Arch:      runtime.GOARCH,
		Agent:     Version,
	}
	if mesh != nil {
		req.WGPublicKey, req.Endpoint = mesh.PublicKey, mesh.Endpoint
	}
	if req.Token == "" {
		return JoinConfig{}, errors.New("a join token is needed")
	}
	body, _ := json.Marshal(req)
	u := strings.TrimRight(panel, "/") + "/api/join/redeem"
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return JoinConfig{}, fmt.Errorf("console address: %w", err)
	}
	hr.Header.Set("content-type", "application/json")
	hr.Header.Set("x-snug-csrf", "1")
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(hr)
	if err != nil {
		return JoinConfig{}, fmt.Errorf("reaching the console: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return JoinConfig{}, fmt.Errorf("console: %s", e.Error)
		}
		return JoinConfig{}, fmt.Errorf("console answered %s", res.Status)
	}
	var cfg JoinConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return JoinConfig{}, fmt.Errorf("console answer: %w", err)
	}
	if cfg.URL == "" || cfg.Region == "" || cfg.Server == "" {
		return JoinConfig{}, errors.New("console answer is missing the address, region or server name")
	}
	if _, err := parseKVURL(cfg.URL, ""); err != nil {
		return JoinConfig{}, fmt.Errorf("console gave a bad SnugKV address: %w", err)
	}
	return cfg, nil
}

// JoinMesh is what Join sends for the mesh.
type JoinMesh struct{ PublicKey, Endpoint string }

// WriteConfig stores the settings in dir (agent.env for systemd's
// EnvironmentFile, ca.pem when there is a CA), readable by root only.
func WriteConfig(dir string, c JoinConfig) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Written by snug-agent join\n")
	line := func(k, v string) { fmt.Fprintf(&b, "%s=%s\n", k, envQuote(v)) }
	line("SNUG_AGENT_URL", c.URL)
	line("SNUG_AGENT_REGION", c.Region)
	line("SNUG_AGENT_SERVER", c.Server)
	if c.MeshIP != "" {
		line("SNUG_AGENT_MESH_IP", c.MeshIP)
	}
	if c.CA != "" {
		ca := filepath.Join(dir, "ca.pem")
		if err := writeFileAtomic(ca, []byte(c.CA), 0o600); err != nil {
			return err
		}
		line("SNUG_AGENT_CA", ca)
	}
	if len(c.MeshPeers) > 0 {
		peers, _ := json.Marshal(c.MeshPeers)
		if err := writeFileAtomic(filepath.Join(dir, MeshBootstrapFile), peers, 0o600); err != nil {
			return err
		}
	}
	return writeFileAtomic(filepath.Join(dir, "agent.env"), []byte(b.String()), 0o600)
}

// MeshBootstrapFile (next to agent.env) holds the peers the console gave at
// join, so the mesh can come up before SnugKV is reachable over it.
const MeshBootstrapFile = "mesh-peers.json"

// envQuote quotes a value for systemd's EnvironmentFile (shell-like double
// quotes). Values never contain line breaks: WriteConfig removes them.
func envQuote(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`", "\n", "", "\r", "")
	return `"` + r.Replace(v) + `"`
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
