package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Swarm node commands, run on a manager (docker CLI): the console uses them to
// drain, demote and remove servers, and to label them (data center, ingress).
//
//   node.ls      every node with its role, availability, state, address and labels
//   node.update  availability (active|pause|drain), role (manager|worker), labels
//   node.rm      removes a node that has left (or, with force, one that is down)

var (
	nodeRefRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	labelKeyRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	labelValRe  = regexp.MustCompile(`^[A-Za-z0-9._:/-]{0,63}$`)
	maxLabelOps = 20
)

type nodeArgs struct {
	Node         string            `json:"node"`
	Availability string            `json:"availability"`
	Role         string            `json:"role"`
	Labels       map[string]string `json:"labels"`
	RemoveLabels []string          `json:"removeLabels"`
	Force        bool              `json:"force"`
}

// NodeRow is one swarm node as node.ls reports it.
type NodeRow struct {
	ID           string            `json:"id"`
	Hostname     string            `json:"hostname"`
	Role         string            `json:"role"`
	Availability string            `json:"availability"`
	State        string            `json:"state"`
	Addr         string            `json:"addr"`
	Leader       bool              `json:"leader,omitempty"`
	Reachability string            `json:"reachability,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
}

func isNodeKind(k string) bool { return strings.HasPrefix(k, "node.") }

func (r *commandRunner) runNode(ctx context.Context, c Command) (any, string, error) {
	if r.dock == nil {
		return nil, "", errors.New("this agent runs without Docker")
	}
	var a nodeArgs
	if len(c.Args) > 0 {
		if err := json.Unmarshal(c.Args, &a); err != nil {
			return nil, "", fmt.Errorf("bad arguments: %w", err)
		}
	}
	needNode := func() error {
		if !nodeRefRe.MatchString(a.Node) {
			return errors.New("invalid node id or name")
		}
		return nil
	}
	switch c.Kind {
	case "node.ls":
		rows, err := r.nodeList(ctx)
		return rows, "", err
	case "node.update":
		if err := needNode(); err != nil {
			return nil, "", err
		}
		args := []string{"node", "update"}
		switch a.Availability {
		case "":
		case "active", "pause", "drain":
			args = append(args, "--availability", a.Availability)
		default:
			return nil, "", errors.New("availability is active, pause or drain")
		}
		switch a.Role {
		case "":
		case "manager", "worker":
			args = append(args, "--role", a.Role)
		default:
			return nil, "", errors.New("role is manager or worker")
		}
		if len(a.Labels)+len(a.RemoveLabels) > maxLabelOps {
			return nil, "", errors.New("too many labels at once")
		}
		keys := make([]string, 0, len(a.Labels))
		for k := range a.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !labelKeyRe.MatchString(k) || !labelValRe.MatchString(a.Labels[k]) {
				return nil, "", fmt.Errorf("label %q: keys use a-z 0-9 . _ -, values A-Z a-z 0-9 . _ : / -", k)
			}
			args = append(args, "--label-add", k+"="+a.Labels[k])
		}
		for _, k := range a.RemoveLabels {
			if !labelKeyRe.MatchString(k) {
				return nil, "", fmt.Errorf("label %q", k)
			}
			args = append(args, "--label-rm", k)
		}
		if len(args) == 2 {
			return nil, "", errors.New("nothing to change")
		}
		args = append(args, a.Node)
		out, err := r.dockerCLI(ctx, time.Minute, nil, "", args...)
		return nil, out, err
	case "node.rm":
		if err := needNode(); err != nil {
			return nil, "", err
		}
		args := []string{"node", "rm"}
		if a.Force {
			args = append(args, "--force")
		}
		out, err := r.dockerCLI(ctx, time.Minute, nil, "", append(args, a.Node)...)
		return nil, out, err
	}
	return nil, "", fmt.Errorf("unknown command %q", c.Kind)
}

func (r *commandRunner) nodeList(ctx context.Context) ([]NodeRow, error) {
	ids, err := r.dockerCLI(ctx, 20*time.Second, nil, "", "node", "ls", "-q")
	if err != nil {
		return nil, err
	}
	list := strings.Fields(ids)
	if len(list) == 0 {
		return []NodeRow{}, nil
	}
	raw, err := r.dockerCLI(ctx, 20*time.Second, nil, "", append([]string{"node", "inspect"}, list...)...)
	if err != nil {
		return nil, err
	}
	var nodes []struct {
		ID   string
		Spec struct {
			Role         string
			Availability string
			Labels       map[string]string
		}
		Description struct{ Hostname string }
		Status      struct {
			State string
			Addr  string
		}
		ManagerStatus *struct {
			Leader       bool
			Reachability string
		}
	}
	if err := json.Unmarshal([]byte(raw), &nodes); err != nil {
		return nil, fmt.Errorf("docker node inspect: %w", err)
	}
	rows := make([]NodeRow, 0, len(nodes))
	for _, n := range nodes {
		row := NodeRow{ID: n.ID, Hostname: n.Description.Hostname, Role: n.Spec.Role, Availability: n.Spec.Availability, State: n.Status.State, Addr: n.Status.Addr, Labels: n.Spec.Labels}
		if n.ManagerStatus != nil {
			row.Leader, row.Reachability = n.ManagerStatus.Leader, n.ManagerStatus.Reachability
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Hostname < rows[j].Hostname })
	return rows, nil
}
