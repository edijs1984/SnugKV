package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerCleanup(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	dock := &swarmDocker{state: "active"}
	cr, err := newCommandRunner(Options{Region: "eu-1", Server: "kv-1", CommandKey: base64.StdEncoding.EncodeToString(pub), StateDir: t.TempDir(), Docker: dock.socket(t)}, nil)
	must(t, err)
	cr.dock = newDockerClient(cr.o.Docker)
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	bin := filepath.Join(dir, "docker")
	must(t, os.WriteFile(bin, []byte(`#!/bin/sh
echo "ARGS $*" >> `+log+`
case "$1 $2" in
  "system df") echo '{"Type":"Images","TotalCount":"9","Active":"3","Size":"4.2GB","Reclaimable":"2.1GB (50%)"}'; echo '{"Type":"Local Volumes","TotalCount":"2","Active":"2","Size":"300MB","Reclaimable":"0B (0%)"}';;
  "image prune") echo "Deleted Images: x"; echo "Total reclaimed space: 2.1GB";;
  "container prune") echo "Total reclaimed space: 12MB";;
  "network prune") echo "";;
  "builder prune") echo "Total reclaimed space: 0B";;
esac
`), 0o755))
	cr.dockerBin = bin

	rows, _, err := cr.run(context.Background(), Command{Kind: "docker.df"})
	must(t, err)
	if d := rows.([]DiskUse); len(d) != 2 || d[0].Type != "Images" || d[0].Total != 9 || d[0].Reclaimable != "2.1GB (50%)" {
		t.Fatalf("df: %+v", rows)
	}
	_, out, err := cr.run(context.Background(), Command{Kind: "docker.cleanup", Args: json.RawMessage(`{"olderThanHours":48}`)})
	must(t, err)
	if !strings.Contains(out, "images 2.1GB") || !strings.Contains(out, "containers 12MB") {
		t.Fatalf("summary: %s", out)
	}
	calls, _ := os.ReadFile(log)
	for _, want := range []string{"ARGS container prune --force", "ARGS image prune --all --force --filter until=48h", "ARGS network prune --force", "ARGS builder prune --force"} {
		if !strings.Contains(string(calls), want) {
			t.Fatalf("missing %q:\n%s", want, calls)
		}
	}
	if strings.Contains(string(calls), "volume") {
		t.Fatal("volumes must never be pruned")
	}
	if _, _, err := cr.run(context.Background(), Command{Kind: "docker.cleanup", Args: json.RawMessage(`{"olderThanHours":-5}`)}); err == nil {
		t.Fatal("bad hours must be refused")
	}
}
