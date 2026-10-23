package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Stacks and services on a swarm manager, run with the docker CLI (installed
// with Docker by the join script). The console sends the compose file; values
// from the secrets module arrive sealed to this server (box.go) and are given
// to `docker stack deploy` as environment, for ${VAR} and `environment: [VAR]`.

var (
	stackNameRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,40}$`)
	serviceNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,120}$`)
	envNameRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	versionLineRe = regexp.MustCompile(`(?m)^version:\s*["']?([0-9.]+)["']?\s*$`)
)

// OverlayMTU fits a VXLAN overlay inside WireGuard (1420 - 50).
const OverlayMTU = "1370"

const maxOutput = 128 << 10

// dockerCLI runs the docker CLI against this server's engine.
func (r *commandRunner) dockerCLI(ctx context.Context, timeout time.Duration, env []string, stdin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	bin := r.dockerBin
	if bin == "" {
		bin = "docker"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	base := []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=" + r.home()}
	base = append(base, "DOCKER_CONFIG="+filepath.Join(r.home(), "docker"))
	if r.o.Docker != "" {
		base = append(base, "DOCKER_HOST=unix://"+r.o.Docker)
	}
	cmd.Env = append(base, env...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	s := out.String()
	if len(s) > maxOutput {
		s = "…(cut)\n" + s[len(s)-maxOutput:]
	}
	if ctx.Err() == context.DeadlineExceeded {
		return s, fmt.Errorf("docker %s took longer than %s", args[0], timeout)
	}
	if err != nil {
		return s, fmt.Errorf("docker %s: %v", strings.Join(args[:min(2, len(args))], " "), err)
	}
	return s, nil
}

// home is where the docker CLI keeps its config (registry logins): the state directory.
func (r *commandRunner) home() string {
	if r.o.StateDir != "" {
		return r.o.StateDir
	}
	return os.TempDir()
}

type stackArgs struct {
	Name     string          `json:"name"`
	Compose  string          `json:"compose"`
	Env      *Sealed         `json:"env"`
	Service  string          `json:"service"`
	Replicas *int            `json:"replicas"`
	Tail     int             `json:"tail"`
	Server   string          `json:"server"`
	Username string          `json:"username"`
	Password *Sealed         `json:"password"`
	Extra    json.RawMessage `json:"extra,omitempty"`
}

// TaskRow is one task of a service (`docker service ps`): where it runs or ran, and why it stopped.
type TaskRow struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	Node    string `json:"node"`
	Desired string `json:"desired"`
	Current string `json:"current"`
	Error   string `json:"error,omitempty"`
}

// ServiceRow is one line of `docker stack services`.
type ServiceRow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Mode     string `json:"mode"`
	Replicas string `json:"replicas"`
	Image    string `json:"image"`
	Ports    string `json:"ports"`
}

func isStackKind(k string) bool {
	return strings.HasPrefix(k, "stack.") || strings.HasPrefix(k, "service.") || k == "registry.login"
}

func (r *commandRunner) runStack(ctx context.Context, c Command) (any, string, error) {
	if r.dock == nil {
		return nil, "", errors.New("this agent runs without Docker")
	}
	var a stackArgs
	if len(c.Args) > 0 {
		if err := json.Unmarshal(c.Args, &a); err != nil {
			return nil, "", fmt.Errorf("bad arguments: %w", err)
		}
	}
	needStack := func() error {
		if !stackNameRe.MatchString(a.Name) {
			return errors.New("stack names use a-z, 0-9, _ and - (up to 41 characters)")
		}
		return nil
	}
	needService := func() error {
		if !serviceNameRe.MatchString(a.Service) {
			return errors.New("invalid service name")
		}
		return nil
	}
	switch c.Kind {
	case "stack.deploy":
		if err := needStack(); err != nil {
			return nil, "", err
		}
		if strings.TrimSpace(a.Compose) == "" || len(a.Compose) > 512<<10 {
			return nil, "", errors.New("the compose file is empty or too big")
		}
		env := []string{}
		if a.Env != nil {
			plain, err := Open(r.box, *a.Env, r.o.Region, r.o.Server)
			if err != nil {
				return nil, "", err
			}
			var vals map[string]string
			if err := json.Unmarshal(plain, &vals); err != nil {
				return nil, "", fmt.Errorf("environment: %w", err)
			}
			keys := make([]string, 0, len(vals))
			for k := range vals {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if envNameRe.MatchString(k) {
					env = append(env, k+"="+vals[k])
				}
			}
		}
		dir, err := os.MkdirTemp("", "snug-stack-")
		if err != nil {
			return nil, "", err
		}
		defer os.RemoveAll(dir)
		composeFile, netFile := filepath.Join(dir, "compose.yml"), filepath.Join(dir, "snug-network.yml")
		if err := os.WriteFile(composeFile, []byte(a.Compose), 0o600); err != nil {
			return nil, "", err
		}
		if err := os.WriteFile(netFile, []byte(networkOverride(a.Compose)), 0o600); err != nil {
			return nil, "", err
		}
		out, err := r.dockerCLI(ctx, 3*time.Minute, env, "", "stack", "deploy", "--with-registry-auth", "--prune", "-c", composeFile, "-c", netFile, a.Name)
		return map[string]int{"envVars": len(env)}, out, err
	case "stack.rm":
		if err := needStack(); err != nil {
			return nil, "", err
		}
		out, err := r.dockerCLI(ctx, time.Minute, nil, "", "stack", "rm", a.Name)
		return nil, out, err
	case "stack.services":
		if err := needStack(); err != nil {
			return nil, "", err
		}
		out, err := r.dockerCLI(ctx, 30*time.Second, nil, "", "stack", "services", "--format", "{{json .}}", a.Name)
		if err != nil {
			if strings.Contains(out, "Nothing found in stack") {
				return []ServiceRow{}, "", nil
			}
			return nil, out, err
		}
		rows := []ServiceRow{}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			var raw map[string]string
			if json.Unmarshal([]byte(line), &raw) == nil {
				rows = append(rows, ServiceRow{ID: raw["ID"], Name: raw["Name"], Mode: raw["Mode"], Replicas: raw["Replicas"], Image: raw["Image"], Ports: raw["Ports"]})
			}
		}
		return rows, "", nil
	case "service.scale":
		if err := needService(); err != nil {
			return nil, "", err
		}
		if a.Replicas == nil || *a.Replicas < 0 || *a.Replicas > 500 {
			return nil, "", errors.New("replicas must be 0 to 500")
		}
		out, err := r.dockerCLI(ctx, time.Minute, nil, "", "service", "scale", "--detach", a.Service+"="+strconv.Itoa(*a.Replicas))
		return nil, out, err
	case "service.restart":
		if err := needService(); err != nil {
			return nil, "", err
		}
		out, err := r.dockerCLI(ctx, time.Minute, nil, "", "service", "update", "--force", "--detach", a.Service)
		return nil, out, err
	case "service.rollback":
		if err := needService(); err != nil {
			return nil, "", err
		}
		out, err := r.dockerCLI(ctx, time.Minute, nil, "", "service", "rollback", "--detach", a.Service)
		return nil, out, err
	case "service.ps":
		if err := needService(); err != nil {
			return nil, "", err
		}
		out, err := r.dockerCLI(ctx, 20*time.Second, nil, "", "service", "ps", "--no-trunc", "--format", "{{json .}}", a.Service)
		if err != nil {
			return nil, out, err
		}
		rows := []TaskRow{}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			var raw map[string]string
			if json.Unmarshal([]byte(line), &raw) == nil {
				rows = append(rows, TaskRow{ID: raw["ID"], Name: raw["Name"], Image: raw["Image"], Node: raw["Node"], Desired: raw["DesiredState"], Current: raw["CurrentState"], Error: raw["Error"]})
			}
		}
		return rows, "", nil
	case "service.logs":
		if err := needService(); err != nil {
			return nil, "", err
		}
		tail := a.Tail
		if tail <= 0 || tail > 1000 {
			tail = 200
		}
		out, err := r.dockerCLI(ctx, 20*time.Second, nil, "", "service", "logs", "--tail", strconv.Itoa(tail), "--timestamps", "--no-trunc", a.Service)
		return nil, out, err
	case "registry.login":
		if a.Password == nil || a.Username == "" || strings.ContainsAny(a.Server+a.Username, " \n\t") {
			return nil, "", errors.New("registry.login needs a server, a user name and a sealed password")
		}
		pw, err := Open(r.box, *a.Password, r.o.Region, r.o.Server)
		if err != nil {
			return nil, "", err
		}
		args := []string{"login", "-u", a.Username, "--password-stdin"}
		if a.Server != "" {
			args = append(args, a.Server)
		}
		out, err := r.dockerCLI(ctx, time.Minute, nil, string(pw), args...)
		return nil, out, err
	}
	return nil, "", fmt.Errorf("unknown command %q", c.Kind)
}

// networkOverride gives the stack's default network an MTU that fits inside
// the WireGuard mesh, merged as a second compose file (same version line as
// the user's file, so the two load the same way).
func networkOverride(compose string) string {
	var b strings.Builder
	if m := versionLineRe.FindStringSubmatch(compose); m != nil {
		fmt.Fprintf(&b, "version: %q\n", m[1])
	}
	b.WriteString("networks:\n  default:\n    driver: overlay\n    driver_opts:\n      com.docker.network.driver.mtu: \"" + OverlayMTU + "\"\n")
	return b.String()
}
