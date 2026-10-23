package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeBinary(t *testing.T, version string) []byte {
	t.Helper()
	return []byte("#!/bin/sh\n[ \"$1\" = -version ] && echo " + version + "\n")
}

func TestSelfUpdate(t *testing.T) {
	newBin := fakeBinary(t, "9.9.9")
	sum := sha256.Sum256(newBin)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(newBin) }))
	defer srv.Close()
	dir := t.TempDir()
	bin := filepath.Join(dir, "snug-agent")
	must(t, os.WriteFile(bin, fakeBinary(t, "0.4.0"), 0o755))
	reqFile := filepath.Join(dir, "update.json")
	write := func(r UpdateRequest) {
		b, _ := json.Marshal(r)
		must(t, os.WriteFile(reqFile, b, 0o644))
	}
	restarted := 0
	restart := func() error { restarted++; return nil }

	// Wrong checksum: nothing changes
	write(UpdateRequest{ID: "u1", URL: srv.URL, SHA256: strings.Repeat("a", 64), RequestedMs: time.Now().UnixMilli()})
	if res := SelfUpdate(context.Background(), reqFile, bin, restart); res.OK || !strings.Contains(res.Message, "SHA-256") {
		t.Fatalf("bad checksum: %+v", res)
	}
	if binVersion(bin) != "0.4.0" || restarted != 0 {
		t.Fatal("the old binary must stay")
	}
	// Too old
	write(UpdateRequest{ID: "u2", URL: srv.URL, SHA256: hex.EncodeToString(sum[:]), RequestedMs: time.Now().Add(-time.Hour).UnixMilli()})
	if res := SelfUpdate(context.Background(), reqFile, bin, restart); res.OK {
		t.Fatal("an old request must be refused")
	}
	// Good
	write(UpdateRequest{ID: "u3", URL: srv.URL, SHA256: hex.EncodeToString(sum[:]), RequestedMs: time.Now().UnixMilli()})
	res := SelfUpdate(context.Background(), reqFile, bin, restart)
	if !res.OK || res.From != "0.4.0" || res.To != "9.9.9" || restarted != 1 {
		t.Fatalf("update: %+v restarted=%d", res, restarted)
	}
	if binVersion(bin) != "9.9.9" {
		t.Fatal("binary not replaced")
	}
	if _, err := os.Stat(reqFile); !os.IsNotExist(err) {
		t.Fatal("request not removed after the update")
	}
	var saved UpdateResult
	b, _ := os.ReadFile(filepath.Join(dir, "update.result"))
	must(t, json.Unmarshal(b, &saved))
	if saved.ID != "u3" || !saved.OK {
		t.Fatalf("result file: %+v", saved)
	}
	// A binary that does not run is not installed
	broken := []byte("not a program")
	bs := sha256.Sum256(broken)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(broken) }))
	defer srv2.Close()
	write(UpdateRequest{ID: "u4", URL: srv2.URL, SHA256: hex.EncodeToString(bs[:]), RequestedMs: time.Now().UnixMilli()})
	if res := SelfUpdate(context.Background(), reqFile, bin, func() error { return errors.New("no") }); res.OK || binVersion(bin) != "9.9.9" {
		t.Fatalf("broken binary: %+v", res)
	}
}

func TestAgentUpdateCommand(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	state := t.TempDir()
	cr, err := newCommandRunner(Options{Region: "eu-1", Server: "a", CommandKey: base64.StdEncoding.EncodeToString(pub), StateDir: state}, nil)
	must(t, err)
	cr.binSHA = strings.Repeat("b", 64)
	args := json.RawMessage(`{"url":"https://example.com/snug-agent-linux-amd64","sha256":"` + strings.Repeat("c", 64) + `"}`)
	if _, out, err := cr.run(context.Background(), Command{ID: "x1", Kind: "agent.update", Args: args}); err != nil || !strings.Contains(out, "requested") {
		t.Fatalf("%s %v", out, err)
	}
	var req UpdateRequest
	b, _ := os.ReadFile(filepath.Join(state, "update.json"))
	must(t, json.Unmarshal(b, &req))
	if req.ID != "x1" || req.SHA256 != strings.Repeat("c", 64) {
		t.Fatalf("request: %+v", req)
	}
	same := json.RawMessage(`{"url":"https://example.com/x","sha256":"` + strings.Repeat("b", 64) + `"}`)
	if _, out, _ := cr.run(context.Background(), Command{ID: "x2", Kind: "agent.update", Args: same}); !strings.Contains(out, "already runs") {
		t.Fatalf("same binary: %s", out)
	}
	for _, a := range []string{`{"url":"ftp://x","sha256":"` + strings.Repeat("c", 64) + `"}`, `{"url":"https://x/y","sha256":"zz"}`} {
		if _, _, err := cr.run(context.Background(), Command{ID: "x3", Kind: "agent.update", Args: json.RawMessage(a)}); err == nil {
			t.Errorf("%s must be refused", a)
		}
	}
}
