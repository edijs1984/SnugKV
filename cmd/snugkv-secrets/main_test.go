package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"

	"snugkv/internal/config"
	"snugkv/internal/engine"
	"snugkv/internal/secrets"
	"snugkv/internal/server"
)

const admin = "admin-token-0123456789abcdef"

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// startModule runs a real server with the secrets module and returns its API URL.
func startModule(t *testing.T) string {
	t.Helper()
	cfg := config.Default()
	cfg.ListenAddr, cfg.AdminAddr = freeAddr(t), freeAddr(t)
	srv, err := server.ListenWithConfig(cfg, engine.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	m, err := secrets.Start(secrets.Options{KVAddr: cfg.ListenAddr, APIListen: "127.0.0.1:0", APIToken: admin, Key: "0123456789abcdef-cli"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return "http://" + m.Addr()
}

func apiDo(t *testing.T, method, url, token, body string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func cli(args ...string) (int, string, string) {
	var o, e bytes.Buffer
	code := realMain(args, &o, &e)
	return code, o.String(), e.String()
}

func TestMergeEnv(t *testing.T) {
	env := mergeEnv([]string{"PORT=8080", "SNUGKV_SECRETS_TOKEN=sks_x_y", "HOME=/h"}, map[string]string{"PORT": "3000", "DB": "mongo"}, false)
	got := strings.Join(env, " ")
	if got != "PORT=8080 HOME=/h DB=mongo" {
		t.Fatalf("keep existing: %s", got)
	}
	env = mergeEnv([]string{"PORT=8080"}, map[string]string{"PORT": "3000"}, true)
	if strings.Join(env, " ") != "PORT=3000" {
		t.Fatalf("override: %v", env)
	}
}

func TestExportGetAndRun(t *testing.T) {
	api := startModule(t)
	apiDo(t, "POST", api+"/v1/envs/app/prod/import", admin, `{"dotenv":"DB=mongodb://x\nGREETING=\"hello world\"\n"}`)
	tok := apiDo(t, "POST", api+"/v1/tokens", admin, `{"name":"app","envs":["app/prod"]}`)["secret"].(string)

	code, out, errOut := cli("export", "--url", api, "--token", tok, "--env", "app/prod")
	if code != 0 || out != "DB=mongodb://x\nGREETING=\"hello world\"\n" {
		t.Fatalf("export = %d %q %q", code, out, errOut)
	}
	code, out, _ = cli("export", "--url", api, "--token", tok, "--env", "app/prod", "--format", "json")
	if code != 0 || !strings.Contains(out, `"GREETING": "hello world"`) {
		t.Fatalf("json export = %d %q", code, out)
	}
	if code, out, _ = cli("get", "--url", api, "--token", tok, "--env", "app/prod", "DB"); code != 0 || out != "mongodb://x\n" {
		t.Fatalf("get = %d %q", code, out)
	}
	if code, _, errOut = cli("get", "--url", api, "--token", tok, "--env", "app/prod", "NOPE"); code != 1 || !strings.Contains(errOut, "not set") {
		t.Fatalf("missing get = %d %q", code, errOut)
	}

	// run: the child sees the secrets, an existing variable wins, the token is gone
	t.Setenv("GREETING", "from compose")
	t.Setenv("SNUGKV_SECRETS_TOKEN", tok)
	t.Setenv("SNUGKV_SECRETS_URL", api)
	outFile := t.TempDir() + "/env.txt"
	code, _, errOut = cli("run", "--env", "app/prod", "--", "sh", "-c", `printf '%s|%s|%s' "$DB" "$GREETING" "${SNUGKV_SECRETS_TOKEN:-none}" > `+outFile)
	if code != 0 {
		t.Fatalf("run = %d %s", code, errOut)
	}
	b, _ := os.ReadFile(outFile)
	if string(b) != "mongodb://x|from compose|none" {
		t.Fatalf("child env = %q", b)
	}
	cli("run", "--env", "app/prod", "--override", "--", "sh", "-c", `printf '%s' "$GREETING" > `+outFile)
	if b, _ = os.ReadFile(outFile); string(b) != "hello world" {
		t.Fatalf("override = %q", b)
	}
	// the child's exit code comes back
	if code, _, _ = cli("run", "--env", "app/prod", "--", "sh", "-c", "exit 7"); code != 7 {
		t.Fatalf("exit code = %d", code)
	}
}

func TestErrorsAreClear(t *testing.T) {
	api := startModule(t)
	if code, _, e := cli("export", "--url", api, "--token", "sks_aaaaaaaaaaaaaaaa_bad", "--env", "app/prod", "--wait", "0s"); code != 1 || !strings.Contains(e, "401") {
		t.Fatalf("bad token = %d %q", code, e)
	}
	if code, _, e := cli("export", "--url", api, "--token", admin, "--env", "app/missing", "--wait", "0s"); code != 1 || !strings.Contains(e, "404") {
		t.Fatalf("missing env = %d %q", code, e)
	}
	if code, _, e := cli("export", "--url", "http://"+freeAddr(t), "--env", "app/prod", "--wait", "0s"); code != 1 || !strings.Contains(e, "cannot reach") {
		t.Fatalf("unreachable = %d %q", code, e)
	}
	if code, _, e := cli("export", "--url", api, "--env", "nope"); code != 1 || !strings.Contains(e, "project/env") {
		t.Fatalf("bad env = %d %q", code, e)
	}
	if code, _, _ := cli("run", "--url", api, "--token", admin, "--env", "app/prod"); code != 1 {
		t.Fatalf("run without command = %d", code)
	}
	if code, _, _ := cli(); code != 2 {
		t.Fatalf("no args = %d", code)
	}
}
