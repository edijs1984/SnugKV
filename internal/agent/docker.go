package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Docker describes the server's Docker engine.
type Docker struct {
	Available  bool        `json:"available"`
	Error      string      `json:"error,omitempty"`
	Version    string      `json:"version,omitempty"`
	Swarm      *Swarm      `json:"swarm,omitempty"`
	Containers []Container `json:"containers"`
	Running    int         `json:"running"`
	Unhealthy  int         `json:"unhealthy"`
}

// Swarm is this node's view of the swarm.
type Swarm struct {
	State    string `json:"state"` // active, inactive, pending, error, locked
	NodeID   string `json:"nodeId,omitempty"`
	Manager  bool   `json:"manager"`
	Managers int    `json:"managers,omitempty"`
	Nodes    int    `json:"nodes,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Container is one container on this server.
type Container struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Image     string `json:"image"`
	State     string `json:"state"`  // running, exited, restarting, …
	Status    string `json:"status"` // "Up 3 hours (healthy)"
	Health    string `json:"health,omitempty"`
	CreatedMs int64  `json:"createdMs"`
	Service   string `json:"service,omitempty"` // swarm service or compose service
	Project   string `json:"project,omitempty"` // swarm stack or compose project
}

type dockerClient struct{ http *http.Client }

func newDockerClient(socket string) *dockerClient {
	return &dockerClient{http: &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}},
	}}
}

func (d *dockerClient) get(path string, out any) error {
	resp, err := d.http.Get("http://docker" + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker %s: HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (d *dockerClient) read() Docker {
	var info struct {
		ServerVersion string
		Swarm         struct {
			LocalNodeState   string
			NodeID           string
			ControlAvailable bool
			Managers         int
			Nodes            int
			Error            string
		}
	}
	if err := d.get("/info", &info); err != nil {
		return Docker{Error: err.Error(), Containers: []Container{}}
	}
	out := Docker{Available: true, Version: info.ServerVersion, Containers: []Container{}}
	if s := info.Swarm; s.LocalNodeState != "" && s.LocalNodeState != "inactive" {
		out.Swarm = &Swarm{State: s.LocalNodeState, NodeID: s.NodeID, Manager: s.ControlAvailable, Managers: s.Managers, Nodes: s.Nodes, Error: s.Error}
	}
	var list []struct {
		ID      string `json:"Id"`
		Names   []string
		Image   string
		State   string
		Status  string
		Created int64
		Labels  map[string]string
	}
	if err := d.get("/containers/json?all=1", &list); err != nil {
		out.Error = err.Error()
		return out
	}
	for _, c := range list {
		ct := Container{ID: shortID(c.ID), Image: c.Image, State: c.State, Status: c.Status, CreatedMs: c.Created * 1000}
		if len(c.Names) > 0 {
			ct.Name = strings.TrimPrefix(c.Names[0], "/")
		}
		switch {
		case strings.Contains(c.Status, "(healthy)"):
			ct.Health = "healthy"
		case strings.Contains(c.Status, "(unhealthy)"):
			ct.Health = "unhealthy"
			out.Unhealthy++
		case strings.Contains(c.Status, "(health: starting)"):
			ct.Health = "starting"
		}
		ct.Service = firstOf(c.Labels, "com.docker.swarm.service.name", "com.docker.compose.service")
		ct.Project = firstOf(c.Labels, "com.docker.stack.namespace", "com.docker.compose.project")
		if c.State == "running" {
			out.Running++
		}
		out.Containers = append(out.Containers, ct)
	}
	sort.Slice(out.Containers, func(i, j int) bool { return out.Containers[i].Name < out.Containers[j].Name })
	return out
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func firstOf(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := m[k]; v != "" {
			return v
		}
	}
	return ""
}

// slow is a client for calls that take long (swarm init/join).
func (d *dockerClient) slow() *http.Client {
	c := *d.http
	c.Timeout = 90 * time.Second
	return &c
}

// post sends JSON to the engine; out may be nil. Errors carry the engine's message.
func (d *dockerClient) post(path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	resp, err := d.slow().Post("http://docker"+path, "application/json", rd)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		var e struct{ Message string }
		if json.Unmarshal(raw, &e) == nil && e.Message != "" {
			return fmt.Errorf("docker: %s", e.Message)
		}
		return fmt.Errorf("docker %s: HTTP %d", path, resp.StatusCode)
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// SwarmTokens are the join tokens of a swarm (managers only can read them).
type SwarmTokens struct {
	NodeID        string `json:"nodeId"`
	WorkerToken   string `json:"workerToken"`
	ManagerToken  string `json:"managerToken"`
	AdvertiseAddr string `json:"advertiseAddr"`
}

func (d *dockerClient) swarmState() (state, nodeID string, manager bool, err error) {
	var info struct {
		Swarm struct {
			LocalNodeState   string
			NodeID           string
			ControlAvailable bool
		}
	}
	if err := d.get("/info", &info); err != nil {
		return "", "", false, err
	}
	return info.Swarm.LocalNodeState, info.Swarm.NodeID, info.Swarm.ControlAvailable, nil
}

func (d *dockerClient) swarmTokens() (SwarmTokens, error) {
	var sw struct {
		JoinTokens struct{ Worker, Manager string }
	}
	if err := d.get("/swarm", &sw); err != nil {
		return SwarmTokens{}, err
	}
	_, id, _, _ := d.swarmState()
	return SwarmTokens{NodeID: id, WorkerToken: sw.JoinTokens.Worker, ManagerToken: sw.JoinTokens.Manager}, nil
}

// swarmInit makes this server the first manager, on its mesh address.
func (d *dockerClient) swarmInit(addr string) (SwarmTokens, error) {
	state, _, manager, err := d.swarmState()
	if err != nil {
		return SwarmTokens{}, err
	}
	if state == "active" {
		if !manager {
			return SwarmTokens{}, errors.New("this server is already a worker in a swarm")
		}
		t, err := d.swarmTokens()
		t.AdvertiseAddr = addr
		return t, err
	}
	req := map[string]any{"ListenAddr": net.JoinHostPort(addr, "2377"), "AdvertiseAddr": addr, "DataPathAddr": addr}
	var nodeID string
	if err := d.post("/swarm/init", req, &nodeID); err != nil {
		return SwarmTokens{}, err
	}
	t, err := d.swarmTokens()
	t.AdvertiseAddr = addr
	return t, err
}

// swarmJoin joins a swarm through one of its managers.
func (d *dockerClient) swarmJoin(addr string, managers []string, token string) error {
	state, _, _, err := d.swarmState()
	if err != nil {
		return err
	}
	if state == "active" {
		return errors.New("this server is already in a swarm; leave it first")
	}
	req := map[string]any{"ListenAddr": net.JoinHostPort(addr, "2377"), "AdvertiseAddr": addr, "DataPathAddr": addr, "RemoteAddrs": managers, "JoinToken": token}
	return d.post("/swarm/join", req, nil)
}

func (d *dockerClient) swarmLeave(force bool) error {
	path := "/swarm/leave"
	if force {
		path += "?force=1"
	}
	return d.post(path, nil, nil)
}
