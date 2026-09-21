package config

import "testing"

func TestOptimizerModeDefaultsToDedicated(t *testing.T) {
	c := Default()
	if c.OptimizerMode != "dedicated" {
		t.Fatalf("optimizer mode = %q, want dedicated", c.OptimizerMode)
	}
}

func TestOptimizerModeValidation(t *testing.T) {
	for _, mode := range []string{"dedicated", "sidecar"} {
		c := Default()
		c.OptimizerMode = mode
		if err := c.Validate(); err != nil {
			t.Fatalf("mode %q rejected: %v", mode, err)
		}
	}

	c := Default()
	c.OptimizerMode = "invalid"
	if err := c.Validate(); err == nil {
		t.Fatal("invalid optimizer mode accepted")
	}
}
