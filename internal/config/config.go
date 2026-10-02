package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"snugkv/internal/resp"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	SourcePath          string `json:"-"`
	MasterUser          string `json:"masteruser"`
	MasterAuth          string `json:"masterauth"`
	MasterTLS           bool   `json:"mastertls"`
	MasterTLSCACert     string `json:"mastertls_ca_cert"`
	MasterTLSCert       string `json:"mastertls_cert"`
	MasterTLSKey        string `json:"mastertls_key"`
	MasterTLSServerName string `json:"mastertls_server_name"`
	AdminAddr           string `json:"admin_listen"`
	EvictionPolicy      string `json:"eviction_policy"`
	MetricsAddr         string `json:"metrics_listen"`
	Compression         bool   `json:"compression"`
	JSONShape           bool   `json:"json_shape"`
	OptimizerMode       string `json:"optimizer_mode"`
	AOFPath             string `json:"aof_path"`
	AOFRewritePath      string `json:"aof_rewrite_path"`
	SnapshotPath        string `json:"snapshot_path"`
	SnapshotIntervalMS  int64  `json:"snapshot_interval_ms"`
	AutoFailoverTimeoutMS int64    `json:"auto_failover_timeout_ms"`
	FailoverPeers          []string `json:"failover_peers"`
	FailoverQuorum         int      `json:"failover_quorum"`
	FailoverPriority       int      `json:"failover_priority"`
	FailoverGroupID        string   `json:"failover_group_id"`
	FailoverConfigEpoch    uint64   `json:"failover_config_epoch"`
	FailoverAdvertiseAddr  string   `json:"failover_advertise_addr"`
	FailoverDiscoverySeeds []string `json:"failover_discovery_seeds"`
	FailoverDiscoveryIntervalMS int64 `json:"failover_discovery_interval_ms"`
	ClusterEnabled        bool              `json:"cluster_enabled"`
	ClusterNodeAddr       string            `json:"cluster_node_addr"`
	ClusterControlAuth    string            `json:"cluster_control_auth"`
	ClusterSlots          map[string]string `json:"cluster_slots"`
	ACLFile             string `json:"acl_file"`
	Fsync               string `json:"fsync"`
	MaxMemory           uint64 `json:"max_memory"`
	GoMemoryLimit       int64  `json:"go_memory_limit"`
	Encoding            bool   `json:"encoding"`
	ListenAddr          string `json:"listen"`
	Shards              int    `json:"shards"`
	MaxConnections      int    `json:"max_connections"`
	ReadTimeoutMS       int64  `json:"read_timeout_ms"`
	WriteTimeoutMS      int64  `json:"write_timeout_ms"`
	MaxRequestBytes     int    `json:"max_request_bytes"`
	MaxBulkBytes        int    `json:"max_bulk_bytes"`
	MaxArguments        int    `json:"max_arguments"`
	CleanupIntervalMS   int64  `json:"cleanup_interval_ms"`
}

func Default() Config {
	return Config{
		AdminAddr:                       "127.0.0.1:6381",
		FailoverDiscoveryIntervalMS:     5000,
		EvictionPolicy:                  "noeviction",
		Fsync:                           "everysec",
		OptimizerMode:                   "dedicated",
		ListenAddr:                      "127.0.0.1:6380",
		Shards:                          256,
		MaxConnections:                  10000,
		ReadTimeoutMS:                   30000,
		WriteTimeoutMS:                  30000,
		MaxRequestBytes:                 64 << 20,
		MaxBulkBytes:                    32 << 20,
		MaxArguments:                    1024,
		CleanupIntervalMS:               100,
		FailoverPriority:                100,
		// SnugKV has one adaptive storage mode by default. Values begin in the
		// cheapest safe representation and background optimization rewrites only
		// when it produces a verified memory win; incompressible values converge
		// to terminal RAW and retain the direct read path.
		Encoding:    true,
		Compression: true,
		JSONShape:   true,
	}
}
func (c Config) Limits() resp.Limits {
	return resp.Limits{MaxRequestBytes: c.MaxRequestBytes, MaxBulkBytes: c.MaxBulkBytes, MaxArguments: c.MaxArguments}
}
func (c Config) Validate() error {
	if c.AOFRewritePath != "" {
		rewrite, err := filepath.Abs(c.AOFRewritePath)
		if err != nil {
			return err
		}
		for _, other := range []string{c.AOFPath, c.SnapshotPath} {
			if other == "" {
				continue
			}
			path, err := filepath.Abs(other)
			if err != nil {
				return err
			}
			if rewrite == path {
				return errors.New("aof_rewrite_path must differ from AOF and snapshot paths")
			}
		}
	}
	if c.ClusterEnabled {
		if c.ClusterNodeAddr == "" {
			return errors.New("cluster_enabled requires cluster_node_addr")
		}
		host, port, err := net.SplitHostPort(c.ClusterNodeAddr)
		if err != nil || host == "" || port == "" {
			return errors.New("cluster_node_addr must be host:port")
		}
		p, err := strconv.Atoi(port)
		if err != nil || p <= 0 || p > 65535 {
			return errors.New("cluster_node_addr must use a valid port")
		}
		if len(c.ClusterSlots) == 0 {
			return errors.New("cluster_enabled requires cluster_slots")
		}
		owned := make([]bool, 16384)
		for rangeText, owner := range c.ClusterSlots {
			start, end, err := parseClusterSlotRangeConfig(rangeText)
			if err != nil {
				return err
			}
			host, port, err := net.SplitHostPort(owner)
			if err != nil || host == "" || port == "" {
				return errors.New("cluster_slots owners must be host:port")
			}
			p, err := strconv.Atoi(port)
			if err != nil || p <= 0 || p > 65535 {
				return errors.New("cluster_slots owners must use a valid port")
			}
			for slot := start; slot <= end; slot++ {
				if owned[slot] {
					return fmt.Errorf("cluster slot %d has multiple owners", slot)
				}
				owned[slot] = true
			}
		}
		if c.ClusterControlAuth == "" {
			return errors.New("cluster_enabled requires cluster_control_auth")
		}
	} else if c.ClusterNodeAddr != "" || c.ClusterControlAuth != "" || len(c.ClusterSlots) > 0 {
		return errors.New("cluster_node_addr, cluster_control_auth and cluster_slots require cluster_enabled")
	}

	if c.FailoverAdvertiseAddr != "" {
		host, port, err := net.SplitHostPort(c.FailoverAdvertiseAddr)
		if err != nil || host == "" || port == "" {
			return errors.New("failover_advertise_addr must be host:port")
		}
		p, err := strconv.Atoi(port)
		if err != nil || p <= 0 || p > 65535 {
			return errors.New("failover_advertise_addr must use a valid port")
		}
	}
	if len(c.FailoverGroupID) > 128 {
		return errors.New("failover_group_id must not exceed 128 bytes")
	}
	if c.FailoverConfigEpoch > 0 && c.FailoverGroupID == "" {
		return errors.New("failover_config_epoch requires failover_group_id")
	}
	if c.FailoverPriority < 0 {
		return errors.New("failover_priority must not be negative")
	}
	if c.FailoverQuorum < 0 {
		return errors.New("failover_quorum must not be negative")
	}
	for _, seed := range c.FailoverDiscoverySeeds {
		host, port, err := net.SplitHostPort(seed)
		if err != nil || host == "" || port == "" {
			return errors.New("failover_discovery_seeds entries must be host:port")
		}
		p, err := strconv.Atoi(port)
		if err != nil || p <= 0 || p > 65535 {
			return errors.New("failover_discovery_seeds entries must use a valid port")
		}
	}
	if len(c.FailoverDiscoverySeeds) > 0 && c.MasterAuth == "" {
		return errors.New("failover_discovery_seeds requires masterauth for authenticated peer RPC")
	}
	if c.FailoverDiscoveryIntervalMS < 100 || c.FailoverDiscoveryIntervalMS > int64((24*time.Hour)/time.Millisecond) {
		return errors.New("failover_discovery_interval_ms must be between 100ms and 24h")
	}
	for _, peer := range c.FailoverPeers {
		host, port, err := net.SplitHostPort(peer)
		if err != nil || host == "" || port == "" {
			return errors.New("failover_peers entries must be host:port")
		}
		p, err := strconv.Atoi(port)
		if err != nil || p <= 0 || p > 65535 {
			return errors.New("failover_peers entries must use a valid port")
		}
	}
	if len(c.FailoverPeers) == 0 && c.FailoverQuorum != 0 {
		return errors.New("failover_quorum requires failover_peers")
	}
	if len(c.FailoverPeers) > 0 && c.MasterAuth == "" {
		return errors.New("failover_peers requires masterauth for authenticated peer RPC")
	}
	if len(c.FailoverPeers) > 0 {
		totalNodes := len(c.FailoverPeers) + 1
		majority := totalNodes/2 + 1
		if c.FailoverQuorum < majority || c.FailoverQuorum > totalNodes {
			return fmt.Errorf("failover_quorum must be between majority (%d) and failover_peers+1", majority)
		}
	}
	if c.AutoFailoverTimeoutMS < 0 {
		return errors.New("auto_failover_timeout_ms must not be negative")
	}
	if c.AutoFailoverTimeoutMS > int64((24*time.Hour)/time.Millisecond) {
		return errors.New("auto_failover_timeout_ms must not exceed 24h")
	}
	if c.SnapshotIntervalMS < 0 {
		return errors.New("snapshot_interval_ms must not be negative")
	}
	if c.SnapshotIntervalMS > 0 && c.SnapshotPath == "" {
		return errors.New("snapshot_interval_ms requires snapshot_path")
	}
	if c.GoMemoryLimit < 0 {
		return errors.New("go_memory_limit must not be negative")
	}
	if c.MasterTLS {
		if c.MasterTLSCACert == "" {
			return errors.New("mastertls requires mastertls_ca_cert")
		}
		if (c.MasterTLSCert == "") != (c.MasterTLSKey == "") {
			return errors.New("mastertls_cert and mastertls_key must be configured together")
		}
	} else if c.MasterTLSCACert != "" || c.MasterTLSCert != "" || c.MasterTLSKey != "" || c.MasterTLSServerName != "" {
		return errors.New("mastertls certificate settings require mastertls")
	}
	if c.AdminAddr != "" && c.AdminAddr == c.ListenAddr {
		return errors.New("admin and public listen addresses must differ")
	}
	if c.MetricsAddr != "" && (c.MetricsAddr == c.ListenAddr || c.MetricsAddr == c.AdminAddr) {
		return errors.New("metrics listen address must differ from RESP listeners")
	}
	if c.EvictionPolicy != "noeviction" && c.EvictionPolicy != "allkeys-lru" && c.EvictionPolicy != "volatile-lru" {
		return errors.New("invalid eviction policy")
	}
	if c.OptimizerMode != "" && c.OptimizerMode != "dedicated" && c.OptimizerMode != "sidecar" {
		return errors.New("optimizer_mode must be dedicated or sidecar")
	}
	if c.AOFPath != "" && c.SnapshotPath != "" {
		a, err := filepath.Abs(c.AOFPath)
		if err != nil {
			return err
		}
		b, err := filepath.Abs(c.SnapshotPath)
		if err != nil {
			return err
		}
		if a == b {
			return errors.New("AOF and snapshot paths must differ")
		}
	}
	if c.AdminAddr != "" {
		host, _, err := net.SplitHostPort(c.AdminAddr)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("admin_listen must use a loopback IP")
		}
	}
	if c.MetricsAddr != "" {
		host, _, err := net.SplitHostPort(c.MetricsAddr)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("metrics_listen must use a loopback IP")
		}
	}

	if (c.JSONShape || c.Compression) && !c.Encoding {
		return errors.New("json_shape and compression require encoding")
	}
	if c.Fsync != "always" && c.Fsync != "everysec" && c.Fsync != "no" {
		return errors.New("invalid fsync policy")
	}
	if c.ListenAddr == "" {
		return errors.New("listen address is required")
	}
	if c.Shards <= 0 || c.Shards&(c.Shards-1) != 0 || c.Shards > 65536 {
		return errors.New("shards must be a power of two between 1 and 65536")
	}
	if c.MaxConnections < 1 {
		return errors.New("max_connections must be positive")
	}
	for _, n := range []int64{c.ReadTimeoutMS, c.WriteTimeoutMS, c.CleanupIntervalMS} {
		if n <= 0 || n > int64((24*time.Hour)/time.Millisecond) {
			return errors.New("timeouts and cleanup interval must be between 1ms and 24h")
		}
	}
	return c.Limits().Validate()
}

// Load uses strict JSON over defaults. Environment overrides are applied separately.
func Load(path string) (Config, error) {
	c := Default()
	c.SourcePath = path

	if path == "" {
		return c, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return c, err
	}
	if st.Size() > 1<<20 {
		return c, errors.New("configuration exceeds 1 MiB")
	}
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, err
	}
	var extra interface{}
	if err = d.Decode(&extra); err != io.EOF {
		return c, errors.New("trailing configuration data")
	}
	return c, nil
}
func (c *Config) ApplyEnv() error {
	if v, ok := os.LookupEnv("SNUGKV_FAILOVER_DISCOVERY_SEEDS"); ok {
		c.FailoverDiscoverySeeds = nil
		for _, seed := range strings.Split(v, ",") {
			seed = strings.TrimSpace(seed)
			if seed != "" {
				c.FailoverDiscoverySeeds = append(c.FailoverDiscoverySeeds, seed)
			}
		}
	}
	if v, ok := os.LookupEnv("SNUGKV_FAILOVER_PEERS"); ok {
		c.FailoverPeers = nil
		for _, peer := range strings.Split(v, ",") {
			peer = strings.TrimSpace(peer)
			if peer != "" {
				c.FailoverPeers = append(c.FailoverPeers, peer)
			}
		}
	}
	if v, ok := os.LookupEnv("SNUGKV_CLUSTER_ENABLED"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return errors.New("invalid SNUGKV_CLUSTER_ENABLED")
		}
		c.ClusterEnabled = b
	}
	if v, ok := os.LookupEnv("SNUGKV_CLUSTER_SLOTS"); ok {
		c.ClusterSlots = make(map[string]string)
		for _, item := range strings.Split(v, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			parts := strings.SplitN(item, "=", 2)
			if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
				return errors.New("invalid SNUGKV_CLUSTER_SLOTS")
			}
			c.ClusterSlots[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	if v, ok := os.LookupEnv("SNUGKV_MASTERTLS"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return errors.New("invalid SNUGKV_MASTERTLS")
		}
		c.MasterTLS = b
	}
	if v, ok := os.LookupEnv("SNUGKV_COMPRESSION"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return errors.New("invalid SNUGKV_COMPRESSION")
		}
		c.Compression = b
	}
	if v, ok := os.LookupEnv("SNUGKV_JSON_SHAPE"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return errors.New("invalid SNUGKV_JSON_SHAPE")
		}
		c.JSONShape = b
	}
	for name, dst := range map[string]*string{"AOF_PATH": &c.AOFPath, "AOF_REWRITE_PATH": &c.AOFRewritePath, "SNAPSHOT_PATH": &c.SnapshotPath, "ACL_FILE": &c.ACLFile, "FSYNC": &c.Fsync, "EVICTION_POLICY": &c.EvictionPolicy, "METRICS_LISTEN": &c.MetricsAddr, "ADMIN_LISTEN": &c.AdminAddr, "OPTIMIZER_MODE": &c.OptimizerMode, "MASTERUSER": &c.MasterUser, "MASTERAUTH": &c.MasterAuth, "FAILOVER_GROUP_ID": &c.FailoverGroupID, "FAILOVER_ADVERTISE_ADDR": &c.FailoverAdvertiseAddr, "CLUSTER_NODE_ADDR": &c.ClusterNodeAddr, "CLUSTER_CONTROL_AUTH": &c.ClusterControlAuth, "MASTERTLS_CA_CERT": &c.MasterTLSCACert, "MASTERTLS_CERT": &c.MasterTLSCert, "MASTERTLS_KEY": &c.MasterTLSKey, "MASTERTLS_SERVER_NAME": &c.MasterTLSServerName} {
		if v, ok := os.LookupEnv("SNUGKV_" + name); ok {
			*dst = v
		}
	}
	if v, ok := os.LookupEnv("SNUGKV_FAILOVER_CONFIG_EPOCH"); ok {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return errors.New("invalid SNUGKV_FAILOVER_CONFIG_EPOCH")
		}
		c.FailoverConfigEpoch = n
	}
	if v, ok := os.LookupEnv("SNUGKV_MAX_MEMORY"); ok {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return errors.New("invalid SNUGKV_MAX_MEMORY")
		}
		c.MaxMemory = n
	}
	if v, ok := os.LookupEnv("SNUGKV_GO_MEMORY_LIMIT"); ok {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return errors.New("invalid SNUGKV_GO_MEMORY_LIMIT")
		}
		c.GoMemoryLimit = n
	}
	if v, ok := os.LookupEnv("SNUGKV_ENCODING"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return errors.New("invalid SNUGKV_ENCODING")
		}
		c.Encoding = b
	}
	if v, ok := os.LookupEnv("SNUGKV_LISTEN"); ok {
		c.ListenAddr = v
	}
	for name, dst := range map[string]*int{"SHARDS": &c.Shards, "MAX_CONNECTIONS": &c.MaxConnections, "MAX_REQUEST_BYTES": &c.MaxRequestBytes, "MAX_BULK_BYTES": &c.MaxBulkBytes, "MAX_ARGUMENTS": &c.MaxArguments, "FAILOVER_QUORUM": &c.FailoverQuorum, "FAILOVER_PRIORITY": &c.FailoverPriority} {
		if v, ok := os.LookupEnv("SNUGKV_" + name); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("invalid SNUGKV_%s", name)
			}
			*dst = n
		}
	}
	for name, dst := range map[string]*int64{"READ_TIMEOUT_MS": &c.ReadTimeoutMS, "WRITE_TIMEOUT_MS": &c.WriteTimeoutMS, "CLEANUP_INTERVAL_MS": &c.CleanupIntervalMS, "SNAPSHOT_INTERVAL_MS": &c.SnapshotIntervalMS, "AUTO_FAILOVER_TIMEOUT_MS": &c.AutoFailoverTimeoutMS, "FAILOVER_DISCOVERY_INTERVAL_MS": &c.FailoverDiscoveryIntervalMS} {
		if v, ok := os.LookupEnv("SNUGKV_" + name); ok {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid SNUGKV_%s", name)
			}
			*dst = n
		}
	}
	return nil
}


func parseClusterSlotRangeConfig(text string) (int, int, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, 0, errors.New("empty cluster slot range")
	}
	if !strings.Contains(text, "-") {
		slot, err := strconv.Atoi(text)
		if err != nil || slot < 0 || slot >= 16384 {
			return 0, 0, fmt.Errorf("invalid cluster slot %q", text)
		}
		return slot, slot, nil
	}
	parts := strings.Split(text, "-")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid cluster slot range %q", text)
	}
	start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid cluster slot range %q", text)
	}
	end, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || start < 0 || end < 0 || start >= 16384 || end >= 16384 || start > end {
		return 0, 0, fmt.Errorf("invalid cluster slot range %q", text)
	}
	return start, end, nil
}
