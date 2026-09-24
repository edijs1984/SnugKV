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
	t.Setenv("SNUGKV_SHARDS", "16")
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


func TestReplicationTLSValidation(t *testing.T) {
	c := Default()
	c.MasterTLS = true
	if err := c.Validate(); err == nil {
		t.Fatal("accepted mastertls without CA certificate")
	}

	c.MasterTLSCACert = "/tmp/ca.pem"
	c.MasterTLSCert = "/tmp/client.crt"
	if err := c.Validate(); err == nil {
		t.Fatal("accepted client certificate without key")
	}

	c.MasterTLSKey = "/tmp/client.key"
	if err := c.Validate(); err != nil {
		t.Fatalf("rejected complete TLS config: %v", err)
	}

	c = Default()
	c.MasterTLSCACert = "/tmp/ca.pem"
	if err := c.Validate(); err == nil {
		t.Fatal("accepted TLS certificate settings while mastertls is disabled")
	}
}

func TestReplicationTLSEnv(t *testing.T) {
	c := Default()
	t.Setenv("SNUGKV_MASTERTLS", "true")
	t.Setenv("SNUGKV_MASTERTLS_CA_CERT", "/tmp/ca.pem")
	t.Setenv("SNUGKV_MASTERTLS_CERT", "/tmp/client.crt")
	t.Setenv("SNUGKV_MASTERTLS_KEY", "/tmp/client.key")
	t.Setenv("SNUGKV_MASTERTLS_SERVER_NAME", "redis.internal")

	if err := c.ApplyEnv(); err != nil {
		t.Fatal(err)
	}
	if !c.MasterTLS ||
		c.MasterTLSCACert != "/tmp/ca.pem" ||
		c.MasterTLSCert != "/tmp/client.crt" ||
		c.MasterTLSKey != "/tmp/client.key" ||
		c.MasterTLSServerName != "redis.internal" {
		t.Fatalf("unexpected TLS env config: %+v", c)
	}
}
