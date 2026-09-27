package config

import (
	"os"
	"testing"
)

func TestFailoverElectionConfigValidation(t *testing.T) {
	cfg := Default()
	cfg.FailoverPeers = []string{"127.0.0.1:6381", "127.0.0.1:6382"}
	cfg.FailoverQuorum = 2
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid failover config: %v", err)
	}

	cfg.FailoverQuorum = 4
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected oversized quorum rejection")
	}

	cfg = Default()
	cfg.FailoverPeers = []string{"bad-peer"}
	cfg.FailoverQuorum = 1
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid peer rejection")
	}
}

func TestFailoverElectionEnv(t *testing.T) {
	keys := []string{"SNUGKV_FAILOVER_PEERS", "SNUGKV_FAILOVER_QUORUM", "SNUGKV_FAILOVER_PRIORITY"}
	old := make(map[string]string)
	had := make(map[string]bool)
	for _, key := range keys {
		old[key], had[key] = os.LookupEnv(key)
	}
	t.Cleanup(func() {
		for _, key := range keys {
			if had[key] {
				_ = os.Setenv(key, old[key])
			} else {
				_ = os.Unsetenv(key)
			}
		}
	})
	_ = os.Setenv("SNUGKV_FAILOVER_PEERS", "127.0.0.1:6381, 127.0.0.1:6382")
	_ = os.Setenv("SNUGKV_FAILOVER_QUORUM", "2")
	_ = os.Setenv("SNUGKV_FAILOVER_PRIORITY", "50")

	cfg := Default()
	if err := cfg.ApplyEnv(); err != nil {
		t.Fatal(err)
	}
	if len(cfg.FailoverPeers) != 2 || cfg.FailoverQuorum != 2 || cfg.FailoverPriority != 50 {
		t.Fatalf("unexpected env config: peers=%v quorum=%d priority=%d", cfg.FailoverPeers, cfg.FailoverQuorum, cfg.FailoverPriority)
	}
}
