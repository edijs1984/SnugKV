// Package agent is snug-agent: it describes the server it runs on (inventory,
// metrics, disks, network, Docker containers and Swarm state) and writes that
// to SnugKV, where the console's Servers page reads it.
//
// Everything is read from /proc, /sys and the Docker socket, so the agent is a
// single static binary with no dependencies. Run it on the host (systemd), or
// in a container with the host's /proc, /sys and / mounted (see Paths).
package agent

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Paths says where the host's files are. On the host they are the defaults;
// in a container mount the host's /proc, /sys and / and point these there.
type Paths struct {
	Proc string // default /proc
	Sys  string // default /sys
	Root string // the host's /, for os-release and disk usage; default /
}

func (p Paths) withDefaults() Paths {
	if p.Proc == "" {
		p.Proc = "/proc"
	}
	if p.Sys == "" {
		p.Sys = "/sys"
	}
	if p.Root == "" {
		p.Root = "/"
	}
	return p
}

// Inventory is what rarely changes.
type Inventory struct {
	Hostname   string `json:"hostname"`
	OS         string `json:"os"`
	Kernel     string `json:"kernel"`
	Arch       string `json:"arch"`
	CPUModel   string `json:"cpuModel"`
	CPUCores   int    `json:"cpuCores"`
	CPUThreads int    `json:"cpuThreads"`
	MemTotal   uint64 `json:"memTotal"`
	SwapTotal  uint64 `json:"swapTotal"`
	BootMs     int64  `json:"bootMs"`
}

// Metrics is one sample. Rates are per second since the previous sample.
type Metrics struct {
	CPU       float64 `json:"cpu"` // percent of all CPUs
	IOWait    float64 `json:"iowait"`
	Load1     float64 `json:"load1"`
	Load5     float64 `json:"load5"`
	Load15    float64 `json:"load15"`
	MemUsed   uint64  `json:"memUsed"` // total - available
	MemTotal  uint64  `json:"memTotal"`
	SwapUsed  uint64  `json:"swapUsed"`
	NetRx     float64 `json:"netRx"` // bytes/s, physical interfaces
	NetTx     float64 `json:"netTx"`
	DiskRead  float64 `json:"diskRead"` // bytes/s, whole disks
	DiskWrite float64 `json:"diskWrite"`
	Procs     int     `json:"procs"`
}

// Disk is one mounted filesystem.
type Disk struct {
	Mount  string `json:"mount"`
	Device string `json:"device"`
	FS     string `json:"fs"`
	Total  uint64 `json:"total"`
	Used   uint64 `json:"used"`
	Inodes uint64 `json:"inodes,omitempty"`
	IUsed  uint64 `json:"inodesUsed,omitempty"`
}

// NIC is one network interface with its rates.
type NIC struct {
	Name string  `json:"name"`
	Rx   float64 `json:"rx"`
	Tx   float64 `json:"tx"`
}

// collector keeps the previous counters to turn them into rates.
type collector struct {
	p        Paths
	prevCPU  []uint64
	prevNet  map[string][2]uint64
	prevDisk [2]uint64
	prevAt   time.Time
}

func newCollector(p Paths) *collector { return &collector{p: p.withDefaults()} }

func (c *collector) path(base string, parts ...string) string {
	return filepath.Join(append([]string{base}, parts...)...)
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// inventory reads the parts that rarely change.
func (c *collector) inventory() Inventory {
	inv := Inventory{
		Hostname: readTrim(c.path(c.p.Proc, "sys/kernel/hostname")),
		Kernel:   readTrim(c.path(c.p.Proc, "sys/kernel/osrelease")),
		Arch:     runtime.GOARCH,
		OS:       osName(c.path(c.p.Root, "etc/os-release")),
	}
	inv.CPUModel, inv.CPUCores, inv.CPUThreads = cpuInfo(c.path(c.p.Proc, "cpuinfo"))
	mem := meminfo(c.path(c.p.Proc, "meminfo"))
	inv.MemTotal, inv.SwapTotal = mem["MemTotal"], mem["SwapTotal"]
	if up := readTrim(c.path(c.p.Proc, "uptime")); up != "" {
		if f, err := strconv.ParseFloat(strings.Fields(up)[0], 64); err == nil {
			inv.BootMs = time.Now().Add(-time.Duration(f * float64(time.Second))).UnixMilli()
		}
	}
	return inv
}

func osName(path string) string {
	vals := map[string]string{}
	for _, line := range strings.Split(readTrim(path), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			vals[k] = strings.Trim(v, `"'`)
		}
	}
	if v := vals["PRETTY_NAME"]; v != "" {
		return v
	}
	return strings.TrimSpace(vals["NAME"] + " " + vals["VERSION_ID"])
}

func cpuInfo(path string) (model string, cores, threads int) {
	f, err := os.Open(path)
	if err != nil {
		return "", runtime.NumCPU(), runtime.NumCPU()
	}
	defer f.Close()
	physical := map[string]bool{}
	var phys, core string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "processor":
			threads++
		case "model name", "Hardware", "cpu model":
			if model == "" {
				model = v
			}
		case "physical id":
			phys = v
		case "core id":
			core = v
			physical[phys+"/"+core] = true
		}
	}
	cores = len(physical)
	if cores == 0 {
		cores = threads
	}
	if threads == 0 {
		threads, cores = runtime.NumCPU(), runtime.NumCPU()
	}
	return model, cores, threads
}

// meminfo returns /proc/meminfo in bytes.
func meminfo(path string) map[string]uint64 {
	out := map[string]uint64{}
	for _, line := range strings.Split(readTrim(path), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fs := strings.Fields(v)
		if len(fs) == 0 {
			continue
		}
		n, err := strconv.ParseUint(fs[0], 10, 64)
		if err != nil {
			continue
		}
		if len(fs) > 1 && fs[1] == "kB" {
			n *= 1024
		}
		out[k] = n
	}
	return out
}

// cpuTimes is the "cpu" line of /proc/stat.
func cpuTimes(path string) ([]uint64, int) {
	var times []uint64
	procs := 0
	for _, line := range strings.Split(readTrim(path), "\n") {
		fs := strings.Fields(line)
		if len(fs) == 0 {
			continue
		}
		switch fs[0] {
		case "cpu":
			for _, x := range fs[1:] {
				n, _ := strconv.ParseUint(x, 10, 64)
				times = append(times, n)
			}
		case "procs_running":
			procs, _ = strconv.Atoi(fs[1])
		}
	}
	return times, procs
}

// virtualNIC: loopback and the interfaces Docker, bridges and VPN tunnels make.
func virtualNIC(name string) bool {
	for _, p := range []string{"lo", "veth", "docker", "br-", "virbr", "vxlan", "ifb", "dummy", "cni", "flannel", "tunl", "kube"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func netCounters(path string) map[string][2]uint64 {
	out := map[string][2]uint64{}
	for _, line := range strings.Split(readTrim(path), "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fs := strings.Fields(rest)
		if len(fs) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(fs[0], 10, 64)
		tx, _ := strconv.ParseUint(fs[8], 10, 64)
		out[strings.TrimSpace(name)] = [2]uint64{rx, tx}
	}
	return out
}

// diskCounters adds read and written bytes of whole disks (not partitions,
// loop or RAM devices): the devices listed in /sys/block.
func (c *collector) diskCounters() [2]uint64 {
	var total [2]uint64
	for _, line := range strings.Split(readTrim(c.path(c.p.Proc, "diskstats")), "\n") {
		fs := strings.Fields(line)
		if len(fs) < 10 {
			continue
		}
		name := fs[2]
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "zram") {
			continue
		}
		if _, err := os.Stat(c.path(c.p.Sys, "block", name)); err != nil {
			continue // a partition
		}
		r, _ := strconv.ParseUint(fs[5], 10, 64)
		w, _ := strconv.ParseUint(fs[9], 10, 64)
		total[0] += r * 512
		total[1] += w * 512
	}
	return total
}

var realFS = map[string]bool{"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true, "f2fs": true, "vfat": true, "ntfs": true, "ntfs3": true, "jfs": true, "reiserfs": true, "bcachefs": true}

// disks lists real filesystems of the host (its init's mount table), one
// entry per device.
func (c *collector) disks() []Disk {
	table := readTrim(c.path(c.p.Proc, "1/mounts"))
	if table == "" {
		table = readTrim(c.path(c.p.Proc, "mounts"))
	}
	seen := map[string]bool{}
	var out []Disk
	for _, line := range strings.Split(table, "\n") {
		fs := strings.Fields(line)
		if len(fs) < 3 || !realFS[fs[2]] || seen[fs[0]] {
			continue
		}
		mount := strings.ReplaceAll(fs[1], `\040`, " ")
		var st syscall.Statfs_t
		if err := syscall.Statfs(c.path(c.p.Root, mount), &st); err != nil || st.Blocks == 0 {
			continue
		}
		seen[fs[0]] = true
		bs := uint64(st.Bsize)
		out = append(out, Disk{
			Mount: mount, Device: fs[0], FS: fs[2],
			Total: st.Blocks * bs, Used: (st.Blocks - st.Bfree) * bs,
			Inodes: st.Files, IUsed: st.Files - st.Ffree,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mount < out[j].Mount })
	return out
}

// sample reads the metrics; rates need a previous sample (zero the first time).
func (c *collector) sample() (Metrics, []NIC) {
	now := time.Now()
	var m Metrics
	secs := now.Sub(c.prevAt).Seconds()
	first := c.prevAt.IsZero()

	times, procs := cpuTimes(c.path(c.p.Proc, "stat"))
	m.Procs = procs
	if len(times) >= 5 && len(c.prevCPU) == len(times) {
		var total, prevTotal uint64
		for i := range times {
			total += times[i]
			prevTotal += c.prevCPU[i]
		}
		idle := times[3] + times[4]
		prevIdle := c.prevCPU[3] + c.prevCPU[4]
		if dt := float64(total - prevTotal); dt > 0 {
			m.CPU = round1(100 * (1 - float64(idle-prevIdle)/dt))
			m.IOWait = round1(100 * float64(times[4]-c.prevCPU[4]) / dt)
		}
	}
	c.prevCPU = times

	if f := strings.Fields(readTrim(c.path(c.p.Proc, "loadavg"))); len(f) >= 3 {
		m.Load1, _ = strconv.ParseFloat(f[0], 64)
		m.Load5, _ = strconv.ParseFloat(f[1], 64)
		m.Load15, _ = strconv.ParseFloat(f[2], 64)
	}
	mem := meminfo(c.path(c.p.Proc, "meminfo"))
	m.MemTotal = mem["MemTotal"]
	if avail, ok := mem["MemAvailable"]; ok && avail <= m.MemTotal {
		m.MemUsed = m.MemTotal - avail
	}
	if mem["SwapFree"] <= mem["SwapTotal"] {
		m.SwapUsed = mem["SwapTotal"] - mem["SwapFree"]
	}

	// The host's network namespace is init's (also when the agent runs in a container with pid: host)
	netPath := c.path(c.p.Proc, "1/net/dev")
	if _, err := os.Stat(netPath); err != nil {
		netPath = c.path(c.p.Proc, "net/dev")
	}
	counters := netCounters(netPath)
	var nics []NIC
	for name, v := range counters {
		if virtualNIC(name) {
			continue
		}
		n := NIC{Name: name}
		if p, ok := c.prevNet[name]; ok && !first && secs > 0 && v[0] >= p[0] && v[1] >= p[1] {
			n.Rx = round1(float64(v[0]-p[0]) / secs)
			n.Tx = round1(float64(v[1]-p[1]) / secs)
		}
		m.NetRx += n.Rx
		m.NetTx += n.Tx
		nics = append(nics, n)
	}
	sort.Slice(nics, func(i, j int) bool { return nics[i].Name < nics[j].Name })
	c.prevNet = counters

	d := c.diskCounters()
	if !first && secs > 0 && d[0] >= c.prevDisk[0] && d[1] >= c.prevDisk[1] {
		m.DiskRead = round1(float64(d[0]-c.prevDisk[0]) / secs)
		m.DiskWrite = round1(float64(d[1]-c.prevDisk[1]) / secs)
	}
	c.prevDisk = d
	c.prevAt = now
	return m, nics
}

func round1(f float64) float64 {
	if f < 0 {
		return 0
	}
	return float64(int64(f*10+0.5)) / 10
}
