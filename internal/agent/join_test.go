package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJoinRedeemsAndWritesConfig(t *testing.T) {
	root := t.TempDir()
	proc := filepath.Join(root, "proc")
	must(t, os.MkdirAll(filepath.Join(proc, "sys/kernel"), 0o755))
	must(t, os.MkdirAll(filepath.Join(root, "etc"), 0o755))
	must(t, os.WriteFile(filepath.Join(proc, "sys/kernel/hostname"), []byte("box-1\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "etc/machine-id"), []byte("abc123\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "etc/os-release"), []byte("PRETTY_NAME=\"Debian GNU/Linux 12\"\n"), 0o644))

	var got JoinRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/join/redeem" || r.Method != http.MethodPost || r.Header.Get("x-snug-csrf") != "1" {
			http.Error(w, `{"error":"bad request"}`, 400)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got.Token != "tok" {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"error":"this join token is not valid (used or expired)"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(JoinConfig{Region: "eu-1", Server: "box-1", URL: `redis://agent:p%22a$s@10.0.0.2:6380`, CA: "PEM"})
	}))
	defer srv.Close()

	paths := Paths{Proc: proc, Sys: filepath.Join(root, "sys"), Root: root}
	cfg, err := Join(context.Background(), srv.URL+"/", " tok\n", "", paths)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hostname != "box-1" || got.MachineID != "abc123" || got.OS != "Debian GNU/Linux 12" || got.Agent != Version {
		t.Fatalf("request = %+v", got)
	}
	dir := filepath.Join(root, "etc/snug-agent")
	must(t, WriteConfig(dir, cfg))
	env, err := os.ReadFile(filepath.Join(dir, "agent.env"))
	must(t, err)
	for _, want := range []string{`SNUG_AGENT_URL="redis://agent:p%22a\$s@10.0.0.2:6380"`, `SNUG_AGENT_REGION="eu-1"`, `SNUG_AGENT_SERVER="box-1"`, `SNUG_AGENT_CA="` + filepath.Join(dir, "ca.pem") + `"`} {
		if !strings.Contains(string(env), want) {
			t.Fatalf("agent.env missing %s:\n%s", want, env)
		}
	}
	st, _ := os.Stat(filepath.Join(dir, "agent.env"))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("agent.env mode %v", st.Mode())
	}

	if _, err := Join(context.Background(), srv.URL, "wrong", "", paths); err == nil || !strings.Contains(err.Error(), "not valid") {
		t.Fatalf("wrong token: %v", err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
