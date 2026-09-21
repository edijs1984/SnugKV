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
	"time"
)

type Config struct {
	SourcePath        string `json:"-"`
	AdminAddr         string `json:"admin_listen"`
	EvictionPolicy    string `json:"eviction_policy"`
	MetricsAddr       string `json:"metrics_listen"`
	Compression       bool   `json:"compression"`
	JSONShape         bool   `json:"json_shape"`
	OptimizerMode     string `json:"optimizer_mode"`
	AOFPath           string `json:"aof_path"`
	SnapshotPath      string `json:"snapshot_path"`
	ACLFile           string `json:"acl_file"`
	Fsync             string `json:"fsync"`
	MaxMemory         uint64 `json:"max_memory"`
	GoMemoryLimit     int64  `json:"go_memory_limit"`
	Encoding          bool   `json:"encoding"`
	ListenAddr        string `json:"listen"`
	Shards            int    `json:"shards"`
	MaxConnections    int    `json:"max_connections"`
	ReadTimeoutMS     int64  `json:"read_timeout_ms"`
	WriteTimeoutMS    int64  `json:"write_timeout_ms"`
	MaxRequestBytes   int    `json:"max_request_bytes"`
	MaxBulkBytes      int    `json:"max_bulk_bytes"`
	MaxArguments      int    `json:"max_arguments"`
	CleanupIntervalMS int64  `json:"cleanup_interval_ms"`
}

func Default() Config {
	return Config{AdminAddr: "127.0.0.1:6381", EvictionPolicy: "noeviction", Fsync: "everysec", OptimizerMode: "dedicated", ListenAddr: "127.0.0.1:6380", Shards: 256, MaxConnections: 10000, ReadTimeoutMS: 30000, WriteTimeoutMS: 30000, MaxRequestBytes: 64 << 20, MaxBulkBytes: 32 << 20, MaxArguments: 1024, CleanupIntervalMS: 100}
}
func (c Config) Limits() resp.Limits {
	return resp.Limits{MaxRequestBytes: c.MaxRequestBytes, MaxBulkBytes: c.MaxBulkBytes, MaxArguments: c.MaxArguments}
}
func (c Config) Validate() error {
	if c.GoMemoryLimit < 0 {
		return errors.New("go_memory_limit must not be negative")
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
	if c.OptimizerMode != "dedicated" && c.OptimizerMode != "sidecar" {
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
	for name, dst := range map[string]*string{"AOF_PATH": &c.AOFPath, "SNAPSHOT_PATH": &c.SnapshotPath, "ACL_FILE": &c.ACLFile, "FSYNC": &c.Fsync, "EVICTION_POLICY": &c.EvictionPolicy, "METRICS_LISTEN": &c.MetricsAddr, "ADMIN_LISTEN": &c.AdminAddr, "OPTIMIZER_MODE": &c.OptimizerMode} {
		if v, ok := os.LookupEnv("SNUGKV_" + name); ok {
			*dst = v
		}
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
	for name, dst := range map[string]*int{"SHARDS": &c.Shards, "MAX_CONNECTIONS": &c.MaxConnections, "MAX_REQUEST_BYTES": &c.MaxRequestBytes, "MAX_BULK_BYTES": &c.MaxBulkBytes, "MAX_ARGUMENTS": &c.MaxArguments} {
		if v, ok := os.LookupEnv("SNUGKV_" + name); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("invalid SNUGKV_%s", name)
			}
			*dst = n
		}
	}
	for name, dst := range map[string]*int64{"READ_TIMEOUT_MS": &c.ReadTimeoutMS, "WRITE_TIMEOUT_MS": &c.WriteTimeoutMS, "CLEANUP_INTERVAL_MS": &c.CleanupIntervalMS} {
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
