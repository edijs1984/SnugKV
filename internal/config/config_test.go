package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaults(t *testing.T) {
	cfg := Default()
	if cfg.ListenAddr == "" {
		t.Fatal("expected default listen addr to be set")
	}
	if cfg.Shards == 0 {
		t.Fatal("expected default shard count to be non-zero")
	}
	if cfg.Shards&(cfg.Shards-1) != 0 {
		t.Fatal("expected default shard count to be a power of two")
	}
}

func TestValidateRejectsNonPowerOfTwoShards(t *testing.T) {
	cfg := Default()
	cfg.Shards = 3
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for non-power-of-two shard count")
	}
}

func TestLoadAndOverrides(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"shards":8,"max_connections":12}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil || c.Shards != 8 || c.MaxConnections != 12 {
		t.Fatalf("%+v %v", c, err)
	}
	t.Setenv("MORPHCACHE_SHARDS", "16")
	if err = c.ApplyEnv(); err != nil || c.Shards != 16 {
		t.Fatal(err)
	}
	for _, data := range []string{`{"unknown":1}`, `{} {}`, `{"shards":"x"}`} {
		os.WriteFile(p, []byte(data), 0600)
		if _, err = Load(p); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
	c = Default()
	c.MaxConnections = 0
	if c.Validate() == nil {
		t.Fatal("connection limit")
	}
}

func TestListenerIsolationValidation(t *testing.T) {
	c := Default()
	c.AdminAddr = c.ListenAddr
	if c.Validate() == nil {
		t.Fatal("accepted shared public/admin address")
	}
	c = Default()
	c.MetricsAddr = c.AdminAddr
	if c.Validate() == nil {
		t.Fatal("accepted shared metrics/admin address")
	}
}
