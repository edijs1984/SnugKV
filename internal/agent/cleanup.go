package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Disk use and clean-up of Docker on this server (any node, not only managers).
//
//   docker.df       what Docker uses and what could be freed: images, containers,
//                   build cache, volumes (volumes are only shown, never removed)
//   docker.cleanup  removes stopped containers, images no container uses that are
//                   older than `olderThanHours` (default 72, so a roll back still finds
//                   the previous image), unused networks and the build cache.
//                   Volumes are never touched: they hold data.

// DiskUse is one line of `docker system df`.
type DiskUse struct {
	Type        string `json:"type"`
	Total       int    `json:"total"`
	Active      int    `json:"active"`
	Size        string `json:"size"`
	Reclaimable string `json:"reclaimable"`
}

type cleanupArgs struct {
	OlderThanHours int `json:"olderThanHours"`
}

var reclaimedRe = regexp.MustCompile(`Total reclaimed space:\s*(\S+)`)

func isDockerKind(k string) bool { return k == "docker.df" || k == "docker.cleanup" }

func (r *commandRunner) runDocker(ctx context.Context, c Command) (any, string, error) {
	if r.dock == nil {
		return nil, "", fmt.Errorf("this agent runs without Docker")
	}
	switch c.Kind {
	case "docker.df":
		rows, err := r.diskUse(ctx)
		return rows, "", err
	case "docker.cleanup":
		var a cleanupArgs
		if len(c.Args) > 0 {
			if err := json.Unmarshal(c.Args, &a); err != nil {
				return nil, "", fmt.Errorf("bad arguments: %w", err)
			}
		}
		hours := a.OlderThanHours
		if hours == 0 {
			hours = 72
		}
		if hours < 1 || hours > 24*365 {
			return nil, "", fmt.Errorf("olderThanHours must be 1 to 8760")
		}
		var out strings.Builder
		freed := []string{}
		step := func(what string, args ...string) {
			o, err := r.dockerCLI(ctx, 10*time.Minute, nil, "", args...)
			if m := reclaimedRe.FindStringSubmatch(o); m != nil {
				freed = append(freed, what+" "+m[1])
			}
			if err != nil {
				fmt.Fprintf(&out, "%s: %v\n", what, err)
			}
		}
		step("containers", "container", "prune", "--force")
		step("images", "image", "prune", "--all", "--force", "--filter", "until="+strconv.Itoa(hours)+"h")
		step("networks", "network", "prune", "--force")
		step("build cache", "builder", "prune", "--force")
		after, _ := r.diskUse(ctx)
		summary := "nothing to free"
		if len(freed) > 0 {
			summary = "freed: " + strings.Join(freed, ", ")
		}
		return map[string]any{"freed": freed, "after": after}, strings.TrimSpace(summary + "\n" + out.String()), nil
	}
	return nil, "", fmt.Errorf("unknown command %q", c.Kind)
}

func (r *commandRunner) diskUse(ctx context.Context) ([]DiskUse, error) {
	o, err := r.dockerCLI(ctx, time.Minute, nil, "", "system", "df", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	rows := []DiskUse{}
	for _, line := range strings.Split(strings.TrimSpace(o), "\n") {
		var raw map[string]string
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		total, _ := strconv.Atoi(raw["TotalCount"])
		active, _ := strconv.Atoi(raw["Active"])
		rows = append(rows, DiskUse{Type: raw["Type"], Total: total, Active: active, Size: raw["Size"], Reclaimable: raw["Reclaimable"]})
	}
	return rows, nil
}
