package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Version is reported on the card.
const Version = "0.6.1"

// Keys in SnugKV (the console's Servers page reads them):
//
//	snug:agent:servers                    ZSET  "<region>/<server>" -> last report (ms)
//	snug:agent:card:<region>:<server>     JSON  inventory + latest sample; expires, so a stopped agent shows as offline
//	snug:agent:metrics:<region>:<server>  STREAM one compact sample per interval, kept for Retention
//	snug:agent:mesh:<region>              HASH  server -> MeshRecord JSON (snug-agent mesh, see mesh.go)
const (
	KeyServers = "snug:agent:servers"
	KeyCard    = "snug:agent:card:"
	KeyMetrics = "snug:agent:metrics:"
	KeyMesh    = "snug:agent:mesh:"
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// Options configure the agent.
type Options struct {
	URL       string // SnugKV address, redis://[user:password@]host:port or rediss://…
	CAFile    string // CA for rediss:// when it is not a public one
	Region    string // e.g. "eu-1"
	Server    string // default: the host name
	Interval  time.Duration
	Retention time.Duration // how long samples are kept (default 24h)
	Paths     Paths
	Docker    string // Docker socket; "" disables, default /var/run/docker.sock
	// Commands from the console: its Ed25519 public key (base64); empty turns commands off
	CommandKey string
	StateDir   string // where the agent keeps the last command it handled (systemd StateDirectory)
	MeshIP     string // this server's mesh address with prefix (swarm commands use it)
	Logf       func(format string, args ...any)
}

// Card is what the console shows for a server.
type Card struct {
	V          int       `json:"v"`
	Region     string    `json:"region"`
	Server     string    `json:"server"`
	Agent      string    `json:"agent"`
	StartedMs  int64     `json:"startedMs"`
	UpdatedMs  int64     `json:"updatedMs"`
	IntervalMs int64     `json:"intervalMs"`
	Inventory  Inventory `json:"inventory"`
	Metrics    Metrics   `json:"metrics"`
	NICs       []NIC     `json:"nics"`
	Disks      []Disk    `json:"disks"`
	Docker     *Docker   `json:"docker,omitempty"`
	Commands   bool      `json:"commands"`         // takes signed commands from the console
	BoxKey     string    `json:"boxKey,omitempty"` // X25519 key the console seals secrets to (box.go)
	// SHA-256 of the agent binary, so the console can tell an update is available
	BinSHA256 string        `json:"binSha256,omitempty"`
	Updater   bool          `json:"updater"`          // the root updater unit is installed
	Update    *UpdateResult `json:"update,omitempty"` // last update by the console
}

// sample is one entry of the metrics stream (short names: it is stored every few seconds).
type sample struct {
	CPU   float64 `json:"c"`
	Mem   uint64  `json:"m"`
	Load  float64 `json:"l"`
	Rx    float64 `json:"rx"`
	Tx    float64 `json:"tx"`
	Read  float64 `json:"rd"`
	Write float64 `json:"wr"`
	Disk  float64 `json:"d"` // fullest disk, percent
	Run   int     `json:"r"` // running containers
}

func (o *Options) normalize() error {
	if o.URL == "" {
		return errors.New("the SnugKV URL is required")
	}
	if o.Server == "" {
		h, _ := os.Hostname()
		o.Server = strings.ToLower(h)
	}
	o.Region, o.Server = strings.ToLower(o.Region), strings.ToLower(o.Server)
	if !nameRe.MatchString(o.Region) || !nameRe.MatchString(o.Server) {
		return errors.New("region and server names use a-z, 0-9, . _ - (up to 63 characters)")
	}
	if o.Interval == 0 {
		o.Interval = 5 * time.Second
	}
	if o.Interval < time.Second {
		return errors.New("the interval must be at least 1s")
	}
	if o.Retention == 0 {
		o.Retention = 24 * time.Hour
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	return nil
}

// Run reports until ctx ends.
func Run(ctx context.Context, o Options) error {
	if err := o.normalize(); err != nil {
		return err
	}
	kv, err := parseKVURL(o.URL, o.CAFile)
	if err != nil {
		return err
	}
	defer kv.close()
	col := newCollector(o.Paths)
	var dock *dockerClient
	if o.Docker != "" {
		dock = newDockerClient(o.Docker)
	}
	card := Card{V: 1, Region: o.Region, Server: o.Server, Agent: Version, StartedMs: time.Now().UnixMilli(), IntervalMs: o.Interval.Milliseconds()}
	card.BinSHA256 = BinarySHA256()
	updateState(o, &card)
	if o.CommandKey != "" {
		if cr, err := newCommandRunner(o, dock); err != nil {
			o.Logf("commands: off: %v", err)
		} else {
			cr.binSHA = card.BinSHA256
			card.Commands, card.BoxKey = true, cr.BoxKey()
			go cr.loop(ctx)
		}
	}
	id := o.Region + "/" + o.Server
	cardKey := KeyCard + o.Region + ":" + o.Server
	streamKey := KeyMetrics + o.Region + ":" + o.Server
	keep := strconv.Itoa(int(o.Retention / o.Interval))
	ttl := strconv.Itoa(int(max(3*o.Interval, 15*time.Second).Seconds()))

	o.Logf("version %s reporting %s every %s", Version, id, o.Interval)
	col.sample() // first counters, so the first report has rates
	var lastInv, lastDisks time.Time
	lastErr := ""
	t := time.NewTicker(o.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			// Gone at once instead of after the card expires
			kv.do("DEL", cardKey)
			return nil
		case <-t.C:
		}
		now := time.Now()
		if now.Sub(lastInv) > 10*time.Minute {
			card.Inventory, lastInv = col.inventory(), now
		}
		if now.Sub(lastDisks) > 30*time.Second {
			card.Disks, lastDisks = col.disks(), now
			updateState(o, &card)
		}
		card.Metrics, card.NICs = col.sample()
		if dock != nil {
			d := dock.read()
			card.Docker = &d
		}
		card.UpdatedMs = now.UnixMilli()

		err := report(kv, &card, id, cardKey, streamKey, keep, ttl)
		switch {
		case err != nil && err.Error() != lastErr:
			o.Logf("cannot report to SnugKV: %v", err)
			lastErr = err.Error()
		case err == nil && lastErr != "":
			o.Logf("reporting again")
			lastErr = ""
		}
	}
}

func report(kv *kvConn, card *Card, id, cardKey, streamKey, keep, ttl string) error {
	b, _ := json.Marshal(card)
	if _, err := kv.do("SET", cardKey, string(b), "EX", ttl); err != nil {
		return err
	}
	if _, err := kv.do("ZADD", KeyServers, strconv.FormatInt(card.UpdatedMs, 10), id); err != nil {
		return err
	}
	s := sample{CPU: card.Metrics.CPU, Mem: card.Metrics.MemUsed, Load: card.Metrics.Load1, Rx: card.Metrics.NetRx, Tx: card.Metrics.NetTx, Read: card.Metrics.DiskRead, Write: card.Metrics.DiskWrite}
	for _, d := range card.Disks {
		if d.Total > 0 {
			if p := round1(100 * float64(d.Used) / float64(d.Total)); p > s.Disk {
				s.Disk = p
			}
		}
	}
	if card.Docker != nil {
		s.Run = card.Docker.Running
	}
	sb, _ := json.Marshal(s)
	if _, err := kv.do("XADD", streamKey, "MAXLEN", "~", keep, "*", "s", string(sb)); err != nil {
		return fmt.Errorf("metrics: %w", err)
	}
	return nil
}

// updateState puts the updater's presence and its last result on the card.
func updateState(o Options, card *Card) {
	if o.StateDir == "" {
		return
	}
	var u UpdateResult
	if b, err := os.ReadFile(filepath.Join(o.StateDir, "update.result")); err == nil && json.Unmarshal(b, &u) == nil {
		card.Update = &u
	}
	_, err := os.Stat("/etc/systemd/system/snug-agent-update.path")
	card.Updater = err == nil
}
