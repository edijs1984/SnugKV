package config

import (
	"strings"
	"testing"
)

func TestClusterControlAuthRequiredForClusterMode(t *testing.T) {
	cfg := Default()
	cfg.ClusterEnabled = true
	cfg.ClusterNodeAddr = "127.0.0.1:7000"
	cfg.ClusterSlots = map[string]string{"0-16383": "127.0.0.1:7000"}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "cluster_enabled requires cluster_control_auth") {
		t.Fatalf("err=%v", err)
	}

	cfg.ClusterControlAuth = "control-secret"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("configured internal control auth rejected: %v", err)
	}
}

func TestClusterControlAuthRequiredForFailoverPeers(t *testing.T) {
	cfg := Default()
	cfg.MasterAuth = "replication-secret"
	cfg.FailoverPeers = []string{"127.0.0.1:7001"}
	cfg.FailoverQuorum = 2

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "failover_peers requires cluster_control_auth") {
		t.Fatalf("err=%v", err)
	}

	cfg.ClusterControlAuth = "control-secret"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("configured failover control auth rejected: %v", err)
	}
}

func TestClusterControlAuthEnvironmentOverride(t *testing.T) {
	t.Setenv("SNUGKV_CLUSTER_CONTROL_AUTH", "env-control-secret")
	cfg := Default()
	if err := cfg.ApplyEnv(); err != nil {
		t.Fatal(err)
	}
	if cfg.ClusterControlAuth != "env-control-secret" {
		t.Fatalf("cluster control auth=%q", cfg.ClusterControlAuth)
	}
}
