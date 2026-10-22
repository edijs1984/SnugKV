package agent

import (
	"context"
	"encoding/json"
	"fmt"
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
