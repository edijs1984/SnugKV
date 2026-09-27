package config

import (
	"path/filepath"
	"testing"
)

func TestAOFRewriteDestination(t *testing.T) {
	for _, kind := range []string{"aof", "snapshot"} {
		t.Run(kind, func(t *testing.T) {
			c := Default()
			c.AOFRewritePath = filepath.Join(t.TempDir(), "data")
			if kind == "aof" {
				c.AOFPath = c.AOFRewritePath
			} else {
				c.SnapshotPath = c.AOFRewritePath
			}
			if err := c.Validate(); err == nil {
				t.Fatal("expected path collision error")
			}
		})
	}
	c := Default()
	c.AOFRewritePath = filepath.Join(t.TempDir(), "export.aof")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.AOFPath != "" {
		t.Fatal("rewrite destination must not enable journaling")
	}
}
