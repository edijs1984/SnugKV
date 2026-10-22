// Command snug-agent reports the server it runs on to SnugKV for the
// console's Servers page. See docs/AGENT.md.
//
//	snug-agent [flags]                       report this server
//	snug-agent join -panel URL -token TOKEN  redeem a join token from the console
//	                                         and write /etc/snug-agent/agent.env
//	snug-agent mesh                          (root) keep this server in the region's WireGuard mesh
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"snugkv/internal/agent"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "join" {
		join(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "mesh" {
		mesh(os.Args[2:])
		return
	}
	var o agent.Options
	flag.StringVar(&o.URL, "url", os.Getenv("SNUG_AGENT_URL"), "SnugKV address: redis://:password@host:6380 or rediss://… (env SNUG_AGENT_URL)")
	flag.StringVar(&o.CAFile, "ca", os.Getenv("SNUG_AGENT_CA"), "CA certificate for rediss:// with a private CA (env SNUG_AGENT_CA)")
	flag.StringVar(&o.Region, "region", envOr("SNUG_AGENT_REGION", "default"), "region of this server (env SNUG_AGENT_REGION)")
	flag.StringVar(&o.Server, "server", os.Getenv("SNUG_AGENT_SERVER"), "name of this server (env SNUG_AGENT_SERVER, default: host name)")
	flag.DurationVar(&o.Interval, "interval", 0, "how often to report (default 5s)")
	flag.DurationVar(&o.Retention, "retention", 0, "how long samples are kept (default 24h)")
	flag.StringVar(&o.Docker, "docker", envOr("SNUG_AGENT_DOCKER", "/var/run/docker.sock"), "Docker socket; empty to skip containers")
	flag.StringVar(&o.Paths.Proc, "proc", os.Getenv("SNUG_AGENT_PROC"), "the host's /proc (in a container: where it is mounted)")
	flag.StringVar(&o.Paths.Sys, "sys", os.Getenv("SNUG_AGENT_SYS"), "the host's /sys")
	flag.StringVar(&o.Paths.Root, "root", os.Getenv("SNUG_AGENT_ROOT"), "the host's / (for disks and os-release)")
	version := flag.Bool("version", false, "print the version")
	flag.Parse()
	if *version {
		fmt.Println(agent.Version)
		return
	}
	if o.Server == "" && o.Paths.Proc != "" {
		// In a container the host's name is in its /proc
		if b, err := os.ReadFile(o.Paths.Proc + "/sys/kernel/hostname"); err == nil {
			o.Server = string(trimNL(b))
		}
	}
	o.Logf = func(format string, args ...any) { log.Printf("snug-agent: "+format, args...) }
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := agent.Run(ctx, o); err != nil {
		log.Fatalf("snug-agent: %v", err)
	}
}

// join redeems a one-time join token (Servers > Add server in the console)
// and stores what the console answers for the systemd unit.
func join(args []string) {
	fs := flag.NewFlagSet("join", flag.ExitOnError)
	panel := fs.String("panel", os.Getenv("SNUG_PANEL"), "console address, e.g. https://panel.example.com")
	token := fs.String("token", os.Getenv("SNUG_JOIN_TOKEN"), "one-time join token")
	name := fs.String("name", "", "server name (default: host name)")
	dir := fs.String("dir", "/etc/snug-agent", "where to write agent.env (and ca.pem, wg.key)")
	endpoint := fs.String("endpoint", os.Getenv("SNUG_AGENT_ENDPOINT"), "public address of this server for the mesh (default: detected)")
	noMesh := fs.Bool("no-mesh", false, "join without the WireGuard mesh")
	_ = fs.Parse(args)
	if *panel == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "usage: snug-agent join -panel https://console -token TOKEN [-name NAME]")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// The mesh key is made here so the console can tell the other servers about this one
	var mesh *agent.JoinMesh
	if !*noMesh {
		pub, err := agent.EnsureKey(filepath.Join(*dir, "wg.key"))
		if err != nil {
			log.Fatalf("snug-agent join: WireGuard key: %v", err)
		}
		ep := *endpoint
		if ep == "" {
			ep = agent.DetectEndpoint(51820)
		} else if _, _, err := net.SplitHostPort(ep); err != nil {
			ep = net.JoinHostPort(ep, "51820")
		}
		if ep != "" {
			mesh = &agent.JoinMesh{PublicKey: pub, Endpoint: ep}
		} else {
			log.Printf("snug-agent join: cannot tell this server's public address; joining without the mesh (use -endpoint)")
		}
	}
	cfg, err := agent.Join(ctx, *panel, *token, *name, agent.Paths{}, mesh)
	if err != nil {
		log.Fatalf("snug-agent join: %v", err)
	}
	if err := agent.WriteConfig(*dir, cfg); err != nil {
		log.Fatalf("snug-agent join: writing %s: %v", *dir, err)
	}
	if cfg.MeshIP != "" {
		fmt.Printf("joined region %s as %s, mesh address %s\n", cfg.Region, cfg.Server, cfg.MeshIP)
	} else {
		fmt.Printf("joined region %s as %s\n", cfg.Region, cfg.Server)
	}
}

// mesh keeps the WireGuard interface and its peers in step with the region
// (needs root). Reads the same settings as the agent, plus the mesh address.
func mesh(args []string) {
	var o agent.MeshOptions
	fs := flag.NewFlagSet("mesh", flag.ExitOnError)
	fs.StringVar(&o.URL, "url", os.Getenv("SNUG_AGENT_URL"), "SnugKV address (env SNUG_AGENT_URL)")
	fs.StringVar(&o.CAFile, "ca", os.Getenv("SNUG_AGENT_CA"), "CA certificate for rediss:// (env SNUG_AGENT_CA)")
	fs.StringVar(&o.Region, "region", envOr("SNUG_AGENT_REGION", "default"), "region (env SNUG_AGENT_REGION)")
	fs.StringVar(&o.Server, "server", os.Getenv("SNUG_AGENT_SERVER"), "server name (env SNUG_AGENT_SERVER, default: host name)")
	fs.StringVar(&o.IP, "ip", os.Getenv("SNUG_AGENT_MESH_IP"), "this server's mesh address with prefix, e.g. 10.88.1.5/24 (env SNUG_AGENT_MESH_IP)")
	fs.StringVar(&o.KeyFile, "key", envOr("SNUG_AGENT_WG_KEY", "/etc/snug-agent/wg.key"), "WireGuard private key, created when missing")
	fs.StringVar(&o.Endpoint, "endpoint", os.Getenv("SNUG_AGENT_ENDPOINT"), "public address other servers reach this one on (default: detected)")
	fs.IntVar(&o.Port, "port", 51820, "WireGuard UDP port")
	fs.StringVar(&o.Iface, "iface", "snug0", "WireGuard interface")
	fs.DurationVar(&o.Interval, "interval", 10*time.Second, "how often to sync")
	_ = fs.Parse(args)
	if o.Server == "" {
		o.Server, _ = os.Hostname()
	}
	o.Logf = func(format string, args ...any) { log.Printf("snug-agent: "+format, args...) }
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := agent.RunMesh(ctx, o); err != nil {
		log.Fatalf("snug-agent mesh: %v", err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
