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
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
	RequestedMs int64  `json:"requestedMs"`
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
