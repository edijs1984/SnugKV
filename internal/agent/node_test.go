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

func TestNodeCommands(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	state := t.TempDir()
	dock := &swarmDocker{state: "active"}
	cr, err := newCommandRunner(Options{Region: "eu-1", Server: "kv-1", CommandKey: base64.StdEncoding.EncodeToString(pub), StateDir: state, Docker: dock.socket(t)}, nil)
	must(t, err)
	cr.dock = newDockerClient(cr.o.Docker)
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	bin := filepath.Join(dir, "docker")
	must(t, os.WriteFile(bin, []byte(`#!/bin/sh
echo "ARGS $*" >> `+log+`
case "$1 $2" in
  "node ls") echo n1; echo n2;;
  "node inspect") echo '[{"ID":"n1","Spec":{"Role":"manager","Availability":"active","Labels":{"dc":"fra-a"}},"Description":{"Hostname":"server1"},"Status":{"State":"ready","Addr":"10.88.1.1"},"ManagerStatus":{"Leader":true,"Reachability":"reachable"}},{"ID":"n2","Spec":{"Role":"worker","Availability":"drain"},"Description":{"Hostname":"server2"},"Status":{"State":"down","Addr":"10.88.1.2"}}]';;
esac
`), 0o755))
	cr.dockerBin = bin

	rows, _, err := cr.run(context.Background(), Command{Kind: "node.ls"})
	must(t, err)
	r := rows.([]NodeRow)
	if len(r) != 2 || r[0].Hostname != "server1" || !r[0].Leader || r[0].Labels["dc"] != "fra-a" || r[1].Availability != "drain" || r[1].State != "down" {
		t.Fatalf("node.ls: %+v", r)
	}
	for _, c := range []struct{ args, want string }{
		{`{"node":"n2","availability":"drain"}`, "ARGS node update --availability drain n2"},
		{`{"node":"n1","role":"worker"}`, "ARGS node update --role worker n1"},
		{`{"node":"n1","labels":{"dc":"fra-b","ingress":"true"},"removeLabels":["old"]}`, "ARGS node update --label-add dc=fra-b --label-add ingress=true --label-rm old n1"},
	} {
		if _, out, err := cr.run(context.Background(), Command{Kind: "node.update", Args: json.RawMessage(c.args)}); err != nil {
			t.Fatalf("%s: %v %s", c.args, err, out)
		}
		if calls, _ := os.ReadFile(log); !strings.Contains(string(calls), c.want) {
			t.Fatalf("missing %q:\n%s", c.want, calls)
		}
	}
	if _, _, err := cr.run(context.Background(), Command{Kind: "node.rm", Args: json.RawMessage(`{"node":"n2","force":true}`)}); err != nil {
		t.Fatal(err)
	}
	if calls, _ := os.ReadFile(log); !strings.Contains(string(calls), "ARGS node rm --force n2") {
		t.Fatalf("node.rm:\n%s", calls)
	}
	for _, c := range []struct{ kind, args string }{
		{"node.update", `{"node":"n1"}`},
		{"node.update", `{"node":"n1","availability":"gone"}`},
		{"node.update", `{"node":"n1","role":"king"}`},
		{"node.update", `{"node":"n1","labels":{"Bad Key":"x"}}`},
		{"node.update", `{"node":"n1","labels":{"dc":"a b"}}`},
		{"node.rm", `{"node":"-rf /"}`},
		{"node.rm", `{"node":"n1;reboot"}`},
	} {
		if _, _, err := cr.run(context.Background(), Command{Kind: c.kind, Args: json.RawMessage(c.args)}); err == nil {
			t.Errorf("%s %s must be refused", c.kind, c.args)
		}
	}

	// agent.remove hands a removal request to the root updater
	if _, _, err := cr.run(context.Background(), Command{ID: "rm-1", Kind: "agent.remove"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(state, "update.json"))
	must(t, err)
	var req UpdateRequest
	must(t, json.Unmarshal(raw, &req))
	if !req.Remove || req.ID != "rm-1" || req.URL != "" {
		t.Fatalf("remove request: %s", raw)
	}
}

func TestUninstall(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"etc/snug-agent/agent.env", "etc/snug/firewall.nft", "usr/local/bin/snug-agent", "etc/systemd/system/snug-agent.service", "var/lib/private/snug-agent/box.key", "etc/ssh/sshd_config.d/00-snug-lockdown.conf"} {
		must(t, os.MkdirAll(filepath.Join(root, filepath.Dir(f)), 0o755))
		must(t, os.WriteFile(filepath.Join(root, f), []byte("x"), 0o600))
	}
	var calls []string
	done := Uninstall(context.Background(), func(_ context.Context, name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}, root)
	if calls[0] != "docker swarm leave --force" || !strings.HasPrefix(calls[1], "systemctl disable --now snug-agent.service snug-mesh.service") {
		t.Fatalf("calls: %q", calls)
	}
	for _, gone := range []string{"etc/snug-agent", "etc/snug", "usr/local/bin/snug-agent", "etc/systemd/system/snug-agent.service", "var/lib/private/snug-agent"} {
		if _, err := os.Stat(filepath.Join(root, gone)); !os.IsNotExist(err) {
			t.Errorf("%s still there", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "etc/ssh/sshd_config.d/00-snug-lockdown.conf")); err != nil {
		t.Error("the SSH settings must stay")
	}
	if len(done) < 5 {
		t.Fatalf("done: %q", done)
	}
}
