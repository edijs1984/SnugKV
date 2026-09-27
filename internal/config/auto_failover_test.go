package config

import (
	"os"
	"testing"
)

func TestAutoFailoverTimeoutValidation(t *testing.T) {
	cfg := Default()
	cfg.AutoFailoverTimeoutMS = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected negative auto failover timeout rejection")
	}

	cfg = Default()
	cfg.AutoFailoverTimeoutMS = 24*60*60*1000 + 1
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected auto failover timeout above 24h rejection")
	}
}

func TestAutoFailoverTimeoutEnv(t *testing.T) {
	const key = "SNUGKV_AUTO_FAILOVER_TIMEOUT_MS"
	old, had := os.LookupEnv(key)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
	if err := os.Setenv(key, "1500"); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	if err := cfg.ApplyEnv(); err != nil {
		t.Fatal(err)
	}
	if cfg.AutoFailoverTimeoutMS != 1500 {
		t.Fatalf("timeout=%d want=1500", cfg.AutoFailoverTimeoutMS)
	}
}
