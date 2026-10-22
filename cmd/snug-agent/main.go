// Command snug-agent reports the server it runs on to SnugKV for the
// console's Servers page. See docs/AGENT.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"snugkv/internal/agent"
)

func main() {
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
