package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"snugkv/internal/config"
	"snugkv/internal/engine"
	"snugkv/internal/server"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeHost builds a /proc, /sys and / like a small server's.
func fakeHost(t *testing.T) Paths {
	dir := t.TempDir()
	p := Paths{Proc: filepath.Join(dir, "proc"), Sys: filepath.Join(dir, "sys"), Root: filepath.Join(dir, "root")}
	write(t, p.Proc, "sys/kernel/hostname", "node-1\n")
	write(t, p.Proc, "sys/kernel/osrelease", "6.8.0-45-generic\n")
	write(t, p.Root, "etc/os-release", "NAME=\"Debian GNU/Linux\"\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n")
	write(t, p.Proc, "cpuinfo", strings.Repeat("", 0)+
		"processor\t: 0\nmodel name\t: AMD EPYC 7502P\nphysical id\t: 0\ncore id\t: 0\n\n"+
		"processor\t: 1\nmodel name\t: AMD EPYC 7502P\nphysical id\t: 0\ncore id\t: 0\n\n"+
		"processor\t: 2\nmodel name\t: AMD EPYC 7502P\nphysical id\t: 0\ncore id\t: 1\n\n"+
		"processor\t: 3\nmodel name\t: AMD EPYC 7502P\nphysical id\t: 0\ncore id\t: 1\n")
	write(t, p.Proc, "meminfo", "MemTotal:       16000000 kB\nMemFree:         2000000 kB\nMemAvailable:    12000000 kB\nSwapTotal:       1000000 kB\nSwapFree:         750000 kB\n")
	write(t, p.Proc, "uptime", "3600.50 7000.00\n")
	write(t, p.Proc, "loadavg", "0.50 0.40 0.30 2/300 1234\n")
	write(t, p.Proc, "stat", "cpu  1000 0 1000 7000 1000 0 0 0 0 0\ncpu0 1 0 0 0 0 0 0 0 0 0\nprocs_running 3\n")
	write(t, p.Proc, "1/net/dev", "Inter-|   Receive\n face |bytes\n    lo: 500 5 0 0 0 0 0 0 500 5 0 0 0 0 0 0\n  eno1: 1000 10 0 0 0 0 0 0 2000 20 0 0 0 0 0 0\n docker0: 999 1 0 0 0 0 0 0 999 1 0 0 0 0 0 0\nveth1a2b: 5 1 0 0 0 0 0 0 5 1 0 0 0 0 0 0\n")
	write(t, p.Proc, "diskstats", "   8       0 sda 100 0 2000 0 50 0 4000 0 0 0 0\n   8       1 sda1 100 0 2000 0 50 0 4000 0 0 0 0\n   7       0 loop0 1 0 8 0 0 0 0 0 0 0 0\n")
	os.MkdirAll(filepath.Join(p.Sys, "block", "sda"), 0o755)
	os.MkdirAll(filepath.Join(p.Sys, "block", "loop0"), 0o755)
	// The root filesystem is the temp dir's real filesystem (statfs needs a real path)
	os.MkdirAll(filepath.Join(p.Root, "data"), 0o755)
	fsType := "ext4"
	write(t, p.Proc, "1/mounts", "/dev/sda1 / "+fsType+" rw 0 0\nproc /proc proc rw 0 0\ntmpfs /run tmpfs rw 0 0\n/dev/sda1 /data "+fsType+" rw 0 0\noverlay /var/lib/docker/overlay2/x/merged overlay rw 0 0\n")
	return p
}

func TestInventory(t *testing.T) {
	p := fakeHost(t)
	inv := newCollector(p).inventory()
	if inv.Hostname != "node-1" || inv.OS != "Debian GNU/Linux 12 (bookworm)" || inv.Kernel != "6.8.0-45-generic" {
		t.Fatalf("inventory = %+v", inv)
	}
	if inv.CPUModel != "AMD EPYC 7502P" || inv.CPUCores != 2 || inv.CPUThreads != 4 {
		t.Fatalf("cpu = %q %d/%d", inv.CPUModel, inv.CPUCores, inv.CPUThreads)
	}
	if inv.MemTotal != 16000000*1024 || inv.SwapTotal != 1000000*1024 {
		t.Fatalf("memory = %d %d", inv.MemTotal, inv.SwapTotal)
	}
	if d := time.Since(time.UnixMilli(inv.BootMs)); d < 3599*time.Second || d > 3602*time.Second {
		t.Fatalf("boot %v ago", d)
	}
}

func TestSampleRatesAndFilters(t *testing.T) {
	p := fakeHost(t)
	c := newCollector(p)
	if m, _ := c.sample(); m.CPU != 0 || m.NetRx != 0 {
		t.Fatalf("first sample has no rates: %+v", m)
	}
	c.prevAt = time.Now().Add(-2 * time.Second) // as if 2 s passed
	// 2 s later: 1000 more busy, 1000 more idle; eno1 +2000 rx, +4000 tx; sda +1000 sectors read
	write(t, p.Proc, "stat", "cpu  1500 0 1500 8000 1000 0 0 0 0 0\nprocs_running 5\n")
	write(t, p.Proc, "1/net/dev", "  eno1: 3000 10 0 0 0 0 0 0 6000 20 0 0 0 0 0 0\n docker0: 9999999 1 0 0 0 0 0 0 9999 1 0 0 0 0 0 0\n")
	write(t, p.Proc, "diskstats", "   8       0 sda 100 0 3000 0 50 0 4000 0 0 0 0\n   8       1 sda1 100 0 99999 0 50 0 4000 0 0 0 0\n")
	m, nics := c.sample()
	if m.CPU != 50 || m.Procs != 5 {
		t.Fatalf("cpu = %v procs = %d", m.CPU, m.Procs)
	}
	if len(nics) != 1 || nics[0].Name != "eno1" {
		t.Fatalf("virtual interfaces must be left out: %+v", nics)
	}
	if m.NetRx < 950 || m.NetRx > 1050 || m.NetTx < 1900 || m.NetTx > 2100 {
		t.Fatalf("net rates = %v / %v per s", m.NetRx, m.NetTx)
	}
	if m.DiskRead < 240000 || m.DiskRead > 270000 { // 1000 sectors * 512 / 2 s; partitions not counted twice
		t.Fatalf("disk read = %v", m.DiskRead)
	}
	if m.MemUsed != (16000000-12000000)*1024 || m.SwapUsed != 250000*1024 || m.Load1 != 0.5 {
		t.Fatalf("memory/load = %+v", m)
	}
}

func TestDisksOnlyRealFilesystemsOncePerDevice(t *testing.T) {
	p := fakeHost(t)
	ds := newCollector(p).disks()
	if len(ds) != 1 || ds[0].Mount != "/" || ds[0].FS != "ext4" || ds[0].Total == 0 {
		t.Fatalf("disks = %+v", ds)
	}
}

// fakeDocker answers /info and /containers/json on a unix socket.
func fakeDocker(t *testing.T) string {
	sock := filepath.Join(t.TempDir(), "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ServerVersion":"27.3.1","Swarm":{"LocalNodeState":"active","NodeID":"n1","ControlAvailable":true,"Managers":3,"Nodes":5}}`))
	})
	mux.HandleFunc("/containers/json", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("all") != "1" {
			t.Errorf("stopped containers must be listed too")
		}
		w.Write([]byte(`[
		 {"Id":"aaaaaaaaaaaaaaaaaaaa","Names":["/airsig_backend.1.x"],"Image":"airsig-backend:local","State":"running","Status":"Up 3 hours (healthy)","Created":1790000000,"Labels":{"com.docker.swarm.service.name":"airsig_backend","com.docker.stack.namespace":"airsig"}},
		 {"Id":"bbbbbbbbbbbbbbbbbbbb","Names":["/ledger"],"Image":"airsig-backend:local","State":"running","Status":"Up 1 minute (unhealthy)","Created":1790000100,"Labels":{"com.docker.compose.service":"ledger","com.docker.compose.project":"airsig-backend"}},
		 {"Id":"cccccccccccccccccccc","Names":["/old"],"Image":"busybox","State":"exited","Status":"Exited (0) 2 days ago","Created":1789000000,"Labels":{}}
		]`))
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return sock
}

func TestDocker(t *testing.T) {
	d := newDockerClient(fakeDocker(t)).read()
	if !d.Available || d.Version != "27.3.1" || d.Running != 2 || d.Unhealthy != 1 || len(d.Containers) != 3 {
		t.Fatalf("docker = %+v", d)
	}
	if d.Swarm == nil || !d.Swarm.Manager || d.Swarm.Nodes != 5 {
		t.Fatalf("swarm = %+v", d.Swarm)
	}
	c := d.Containers[0]
	if c.Name != "airsig_backend.1.x" || c.ID != "aaaaaaaaaaaa" || c.Health != "healthy" || c.Service != "airsig_backend" || c.Project != "airsig" {
		t.Fatalf("container = %+v", c)
	}
	if missing := newDockerClient(filepath.Join(t.TempDir(), "none.sock")).read(); missing.Available || missing.Error == "" {
		t.Fatalf("no docker = %+v", missing)
	}
}

func TestParseURL(t *testing.T) {
	k, err := parseKVURL("redis://:pw@10.0.0.5:6380", "")
	if err != nil || k.addr != "10.0.0.5:6380" || k.pass != "pw" || k.user != "" || k.tls != nil {
		t.Fatalf("%+v %v", k, err)
	}
	k, _ = parseKVURL("rediss://agent:pw@kv.example:6390", "")
	if k.user != "agent" || k.pass != "pw" || k.tls == nil || k.tls.ServerName != "kv.example" {
		t.Fatalf("%+v", k)
	}
	k, _ = parseKVURL("redis://secret@host", "")
	if k.pass != "secret" || k.addr != "host:6379" {
		t.Fatalf("password-only URL: %+v", k)
	}
	if _, err := parseKVURL("http://x", ""); err == nil {
		t.Fatal("http:// must be refused")
	}
}

func TestRunReportsToSnugKV(t *testing.T) {
	c := config.Default()
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	c.ListenAddr = l.Addr().String()
	l.Close()
	l, _ = net.Listen("tcp", "127.0.0.1:0")
	c.AdminAddr = l.Addr().String()
	l.Close()
	c.RequirePass = "agent-pw-0123"
	srv, err := server.ListenWithConfig(c, engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{URL: "redis://:agent-pw-0123@" + c.ListenAddr, Region: "EU-1", Server: "node-1", Interval: time.Second, Paths: fakeHost(t), Docker: fakeDocker(t)})
	}()

	kv, _ := parseKVURL("redis://:agent-pw-0123@"+c.ListenAddr, "")
	defer kv.close()
	var card Card
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, _ := kv.do("GET", KeyCard+"eu-1:node-1")
		if raw != "" {
			json.Unmarshal([]byte(raw), &card)
			if n, _ := kv.do("XLEN", KeyMetrics+"eu-1:node-1"); n >= "2" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("no card / metrics after 5 s")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if card.Region != "eu-1" || card.Inventory.Hostname != "node-1" || card.Docker == nil || card.Docker.Running != 2 || len(card.Disks) != 1 {
		t.Fatalf("card = %+v", card)
	}
	if score, _ := kv.do("ZSCORE", KeyServers, "eu-1/node-1"); score == "" {
		t.Fatal("server not listed")
	}
	if ttl, _ := kv.do("TTL", KeyCard+"eu-1:node-1"); ttl == "-1" || ttl == "" {
		t.Fatalf("the card must expire (ttl %s)", ttl)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if raw, _ := kv.do("GET", KeyCard+"eu-1:node-1"); raw != "" {
		t.Fatal("a stopped agent removes its card")
	}
	if err := Run(context.Background(), Options{URL: "redis://x:1", Region: "bad region"}); err == nil {
		t.Fatal("a bad region name must be refused")
	}
}
