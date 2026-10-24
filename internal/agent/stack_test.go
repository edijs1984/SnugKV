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

func TestSealOpen(t *testing.T) {
	dir := t.TempDir()
	k, err := EnsureBoxKey(dir)
	must(t, err)
	k2, err := EnsureBoxKey(dir)
	must(t, err)
	if !k.Equal(k2) {
		t.Fatal("box key not kept")
	}
	s, err := Seal(k.PublicKey(), []byte(`{"A":"1"}`), "eu-1", "kv-1")
	must(t, err)
	if out, err := Open(k, s, "eu-1", "kv-1"); err != nil || string(out) != `{"A":"1"}` {
		t.Fatalf("open: %s %v", out, err)
	}
	if _, err := Open(k, s, "eu-1", "app-1"); err == nil {
		t.Fatal("a value sealed for kv-1 must not open as app-1")
	}
	other, _ := EnsureBoxKey(t.TempDir())
	if _, err := Open(other, s, "eu-1", "kv-1"); err == nil {
		t.Fatal("another key must not open it")
	}
}

func TestNetworkOverride(t *testing.T) {
	if got := networkOverride("version: \"3.8\"\nservices: {}\n"); !strings.HasPrefix(got, "version: \"3.8\"\n") || !strings.Contains(got, `com.docker.network.driver.mtu: "1370"`) {
		t.Fatalf("%s", got)
	}
	if got := networkOverride("services: {}\n"); strings.Contains(got, "version") {
		t.Fatalf("no version line expected: %s", got)
	}
}

// fakeCLI records each docker call (args, env) and answers like docker would.
func fakeCLI(t *testing.T) (bin, log string) {
	dir := t.TempDir()
	log = filepath.Join(dir, "calls.log")
	bin = filepath.Join(dir, "docker")
	script := `#!/bin/sh
{ echo "ARGS $*"; env | grep -E '^(DB_PASSWORD|API_KEY|DOCKER_HOST|DOCKER_CONFIG)=' | sort; [ "$1" = login ] && echo "STDIN $(cat)"; [ "$1" = stack ] && [ "$2" = deploy ] && for f in "$@"; do case "$f" in *.yml) echo "FILE $f"; cat "$f";; esac; done; } >> ` + log + `
case "$1 $2" in
  "stack services") echo '{"ID":"abc","Name":"web_api","Mode":"replicated","Replicas":"2/2","Image":"nginx:1","Ports":"*:80->80/tcp"}';;
  "stack deploy") echo "Creating service web_api";;
  "service ps") echo '{"ID":"t1","Name":"web_api.1","Image":"nginx:1","Node":"kv-1","DesiredState":"Running","CurrentState":"Running 2 minutes ago","Error":""}'; echo '{"ID":"t0","Name":"web_api.1","Image":"nginx:1","Node":"kv-2","DesiredState":"Shutdown","CurrentState":"Failed 3 minutes ago","Error":"task: non-zero exit (1)"}';;
esac
`
	must(t, os.WriteFile(bin, []byte(script), 0o755))
	return bin, log
}

func TestStackCommands(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	state := t.TempDir()
	dock := &swarmDocker{state: "active"}
	cr, err := newCommandRunner(Options{Region: "eu-1", Server: "kv-1", CommandKey: base64.StdEncoding.EncodeToString(pub), StateDir: state, Docker: dock.socket(t)}, nil)
	must(t, err)
	cr.dock = newDockerClient(cr.o.Docker)
	bin, log := fakeCLI(t)
	cr.dockerBin = bin
	box, _ := EnsureBoxKey(state)
	env, err := Seal(box.PublicKey(), []byte(`{"DB_PASSWORD":"s3cr3t","API_KEY":"k","bad name":"x"}`), "eu-1", "kv-1")
	must(t, err)
	args, _ := json.Marshal(map[string]any{"name": "web", "compose": "version: \"3.8\"\nservices:\n  api:\n    image: nginx:1\n    environment: [DB_PASSWORD]\n", "env": env})
	data, out, err := cr.run(context.Background(), Command{Kind: "stack.deploy", Args: args})
	if err != nil {
		t.Fatalf("deploy: %v %s", err, out)
	}
	if !strings.Contains(out, "Creating service") || data.(map[string]int)["envVars"] != 2 {
		t.Fatalf("deploy: %v %s", data, out)
	}
	calls, _ := os.ReadFile(log)
	for _, want := range []string{"ARGS stack deploy --with-registry-auth --prune -c", " web", "DB_PASSWORD=s3cr3t", "API_KEY=k", "DOCKER_HOST=unix://", "DOCKER_CONFIG=" + filepath.Join(state, "docker"), `com.docker.network.driver.mtu: "1370"`, "image: nginx:1"} {
		if !strings.Contains(string(calls), want) {
			t.Fatalf("docker call missing %q:\n%s", want, calls)
		}
	}
	if strings.Contains(string(calls), "bad name") {
		t.Fatal("invalid environment names must be dropped")
	}

	rows, _, err := cr.run(context.Background(), Command{Kind: "stack.services", Args: json.RawMessage(`{"name":"web"}`)})
	must(t, err)
	if r := rows.([]ServiceRow); len(r) != 1 || r[0].Replicas != "2/2" || r[0].Name != "web_api" {
		t.Fatalf("services: %+v", rows)
	}
	tasks, _, err := cr.run(context.Background(), Command{Kind: "service.ps", Args: json.RawMessage(`{"service":"web_api"}`)})
	must(t, err)
	if tr := tasks.([]TaskRow); len(tr) != 2 || tr[0].Node != "kv-1" || tr[1].Error != "task: non-zero exit (1)" || tr[1].Desired != "Shutdown" {
		t.Fatalf("service.ps: %+v", tasks)
	}
	for _, c := range []struct{ kind, args, want string }{
		{"service.scale", `{"service":"web_api","replicas":3}`, "ARGS service scale --detach web_api=3"},
		{"service.restart", `{"service":"web_api"}`, "ARGS service update --force --detach web_api"},
		{"service.rollback", `{"service":"web_api"}`, "ARGS service rollback --detach web_api"},
		{"service.logs", `{"service":"web_api","tail":50}`, "ARGS service logs --tail 50 --timestamps --no-trunc web_api"},
		{"stack.rm", `{"name":"web"}`, "ARGS stack rm web"},
	} {
		if _, out, err := cr.run(context.Background(), Command{Kind: c.kind, Args: json.RawMessage(c.args)}); err != nil {
			t.Fatalf("%s: %v %s", c.kind, err, out)
		}
		if calls, _ := os.ReadFile(log); !strings.Contains(string(calls), c.want) {
			t.Fatalf("%s: missing %q", c.kind, c.want)
		}
	}
	pw, _ := Seal(box.PublicKey(), []byte("hub-token"), "eu-1", "kv-1")
	la, _ := json.Marshal(map[string]any{"server": "registry.example.com", "username": "edijs", "password": pw})
	if _, out, err := cr.run(context.Background(), Command{Kind: "registry.login", Args: la}); err != nil {
		t.Fatalf("login: %v %s", err, out)
	}
	if calls, _ := os.ReadFile(log); !strings.Contains(string(calls), "ARGS login -u edijs --password-stdin registry.example.com") || !strings.Contains(string(calls), "STDIN hub-token") {
		t.Fatalf("login call:\n%s", calls)
	}
	// Refusals
	for _, c := range []struct{ kind, args string }{
		{"stack.deploy", `{"name":"Bad Name","compose":"x"}`},
		{"stack.deploy", `{"name":"web","compose":"  "}`},
		{"service.scale", `{"service":"web_api","replicas":9999}`},
		{"service.logs", `{"service":"web;rm -rf"}`},
	} {
		if _, _, err := cr.run(context.Background(), Command{Kind: c.kind, Args: json.RawMessage(c.args)}); err == nil {
			t.Errorf("%s %s must be refused", c.kind, c.args)
		}
	}
	wrong, _ := Seal(box.PublicKey(), []byte(`{}`), "eu-1", "other")
	wa, _ := json.Marshal(map[string]any{"name": "web", "compose": "services: {}", "env": wrong})
	if _, _, err := cr.run(context.Background(), Command{Kind: "stack.deploy", Args: wa}); err == nil {
		t.Fatal("env sealed for another server must be refused")
	}
}

func TestPlacementOverride(t *testing.T) {
	compose := "version: '3.8'\n# app\nservices:\n  api:\n    image: x\n    environment:\n      - A\n  \"worker\":\n    image: y\n\nnetworks:\n  web:\n    external: true\n"
	if got := composeServices(compose); strings.Join(got, ",") != "api,worker" {
		t.Fatalf("services: %v", got)
	}
	out, err := placementOverride(compose, []string{"node.role==worker", "node.labels.snug.airsig-api==1"})
	if err != nil {
		t.Fatal(err)
	}
	want := "version: \"3.8\"\nservices:\n  \"api\":\n    deploy:\n      placement:\n        constraints:\n          - \"node.role==worker\"\n          - \"node.labels.snug.airsig-api==1\"\n  \"worker\":\n    deploy:\n      placement:\n        constraints:\n          - \"node.role==worker\"\n          - \"node.labels.snug.airsig-api==1\"\n"
	if out != want {
		t.Fatalf("override:\n%s", out)
	}
	for _, bad := range []string{"node.role==worker; rm -rf /", "engine.labels.x==1", "node.labels.X==1", "node.role=worker"} {
		if _, err := placementOverride(compose, []string{bad}); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	if _, err := placementOverride("services: {}\n", []string{"node.role==worker"}); err == nil {
		t.Fatal("no services accepted")
	}
}
