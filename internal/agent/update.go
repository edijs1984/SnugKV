package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Updating the agent from the console.
//
// The reporting agent is unprivileged, so it cannot replace its own binary.
// On the console's signed `agent.update` command it writes update.json (URL and
// SHA-256 of the new binary) to its state directory; a root unit watching that
// file (snug-agent-update.path, PathExists, installed by the join script) runs
// `snug-agent self-update`, which downloads the binary, checks the SHA-256,
// checks that it runs, swaps it in and restarts snug-agent and snug-mesh.

// UpdateRequest is what the agent writes for the root updater.
type UpdateRequest struct {
	ID          string `json:"id"`
	URL         string `json:"url,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	RequestedMs int64  `json:"requestedMs"`
	// Remove: take the agent off this server instead (agent.remove, see Uninstall)
	Remove bool `json:"remove,omitempty"`
}

// UpdateResult is written next to the request by the updater.
type UpdateResult struct {
	ID       string `json:"id"`
	OK       bool   `json:"ok"`
	Message  string `json:"message"`
	From     string `json:"from"`
	To       string `json:"to"`
	FinishMs int64  `json:"finishedMs"`
}

var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

func checkUpdate(req UpdateRequest) error {
	if req.Remove {
		return nil
	}
	u, err := url.Parse(req.URL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("the download address must be an http(s) URL")
	}
	if !sha256Re.MatchString(req.SHA256) {
		return errors.New("the SHA-256 must be 64 hex characters")
	}
	return nil
}

// BinarySHA256 is the SHA-256 of the running binary (shown on the card, so
// the console can tell whether a release is newer than what runs).
func BinarySHA256() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	f, err := os.Open(exe)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// requestUpdate is the unprivileged half: it hands the request to the updater.
func (r *commandRunner) requestUpdate(c Command) (any, string, error) {
	var req UpdateRequest
	if err := json.Unmarshal(c.Args, &req); err != nil {
		return nil, "", fmt.Errorf("bad arguments: %w", err)
	}
	req.ID, req.RequestedMs = c.ID, time.Now().UnixMilli()
	if err := checkUpdate(req); err != nil {
		return nil, "", err
	}
	if r.o.StateDir == "" {
		return nil, "", errors.New("this agent has no state directory (it was not installed by the join script)")
	}
	if req.SHA256 == r.binSHA {
		return map[string]bool{"current": true}, "this server already runs that binary", nil
	}
	b, _ := json.Marshal(req)
	if err := writeFileAtomic(filepath.Join(r.o.StateDir, "update.json"), b, 0o644); err != nil {
		return nil, "", err
	}
	return map[string]bool{"requested": true}, "update requested: the updater downloads and checks the new agent, then restarts it (about a minute)", nil
}

// requestRemove hands "take the agent off this server" to the root updater.
func (r *commandRunner) requestRemove(c Command) (any, string, error) {
	if r.o.StateDir == "" {
		return nil, "", errors.New("this agent has no state directory (it was not installed by the join script)")
	}
	b, _ := json.Marshal(UpdateRequest{ID: c.ID, RequestedMs: time.Now().UnixMilli(), Remove: true})
	if err := writeFileAtomic(filepath.Join(r.o.StateDir, "update.json"), b, 0o644); err != nil {
		return nil, "", err
	}
	return map[string]bool{"requested": true}, "removal requested: the agent leaves the swarm, stops and deletes itself (Docker and the SSH settings stay)", nil
}

// Uninstall takes the platform off this server (root): leaves the swarm, stops and
// deletes the agent, mesh, updater and firewall units, the WireGuard interface,
// the agent's settings, state and binary. Docker, its containers' images and the
// SSH settings stay. root stands in for / in tests. Returns what was done.
func Uninstall(ctx context.Context, run func(ctx context.Context, name string, args ...string) error, root string) []string {
	var done []string
	try := func(what, name string, args ...string) {
		if run(ctx, name, args...) == nil {
			done = append(done, what)
		}
	}
	try("left the swarm", "docker", "swarm", "leave", "--force")
	units := []string{"snug-agent.service", "snug-mesh.service", "snug-firewall.service", "snug-agent-update.path", "snug-agent-update.service"}
	try("stopped the units", "systemctl", append([]string{"disable", "--now"}, units...)...)
	try("removed the mesh interface", "ip", "link", "delete", "snug0")
	try("removed the firewall table", "nft", "delete", "table", "inet", "snug")
	for _, f := range []string{
		"/etc/systemd/system/snug-agent.service", "/etc/systemd/system/snug-mesh.service", "/etc/systemd/system/snug-firewall.service",
		"/etc/systemd/system/snug-agent-update.path", "/etc/systemd/system/snug-agent-update.service",
		"/etc/modules-load.d/snug-wireguard.conf", "/etc/snug-agent", "/etc/snug", "/var/lib/private/snug-agent", "/var/lib/snug-agent",
		"/usr/local/bin/snug-agent",
	} {
		_ = os.RemoveAll(filepath.Join(root, f))
	}
	done = append(done, "deleted the agent's files")
	try("reloaded systemd", "systemctl", "daemon-reload")
	return done
}

func runCommand(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

// SelfUpdate is the root half (`snug-agent self-update`): download, check,
// swap, restart. Its result goes to update.result next to the request.
func SelfUpdate(ctx context.Context, requestFile, binPath string, restart func() error) (res UpdateResult) {
	res.FinishMs = time.Now().UnixMilli()
	defer func() {
		// Done either way: the path unit (PathExists) must not fire again for it
		_ = os.Remove(requestFile)
		res.FinishMs = time.Now().UnixMilli()
		b, _ := json.Marshal(res)
		_ = os.WriteFile(filepath.Join(filepath.Dir(requestFile), "update.result"), b, 0o644)
	}()
	raw, err := os.ReadFile(requestFile)
	if err != nil {
		res.Message = err.Error()
		return
	}
	var req UpdateRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		res.Message = "unreadable request: " + err.Error()
		return
	}
	res.ID = req.ID
	if err := checkUpdate(req); err != nil {
		res.Message = err.Error()
		return
	}
	if time.Since(time.UnixMilli(req.RequestedMs)) > 15*time.Minute {
		res.Message = "request too old"
		return
	}
	if req.Remove {
		steps := Uninstall(ctx, runCommand, "")
		res.OK, res.Message = true, "removed: "+strings.Join(steps, "; ")
		return
	}
	res.From = binVersion(binPath)
	tmp := binPath + ".new"
	if err := download(ctx, req.URL, req.SHA256, tmp); err != nil {
		res.Message = err.Error()
		return
	}
	defer os.Remove(tmp)
	ver := binVersion(tmp)
	if ver == "" {
		res.Message = "the downloaded agent does not run here"
		return
	}
	if err := os.Rename(tmp, binPath); err != nil {
		res.Message = err.Error()
		return
	}
	res.To = ver
	if err := restart(); err != nil {
		res.Message = "installed " + ver + ", but the restart failed: " + err.Error()
		return
	}
	res.OK, res.Message = true, fmt.Sprintf("updated from %s to %s", res.From, ver)
	return
}

func download(ctx context.Context, rawURL, want, dst string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 200<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
		return fmt.Errorf("download: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(dst)
		return fmt.Errorf("the download's SHA-256 is %s, expected %s", got, want)
	}
	return os.Chmod(dst, 0o755)
}

func binVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// RestartServices restarts the agent units (the mesh unit only when installed).
func RestartServices() error {
	units := []string{"snug-agent.service"}
	if exec.Command("systemctl", "cat", "snug-mesh.service").Run() == nil {
		units = append(units, "snug-mesh.service")
	}
	out, err := exec.Command("systemctl", append([]string{"restart"}, units...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
