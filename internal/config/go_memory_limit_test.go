package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGoMemoryLimitFromJSON(t *testing.T) {
	path := filepath.Join(
		t.TempDir(),
		"snugkv.json",
	)

	const limit = int64(128 * 1024 * 1024)

	if err := os.WriteFile(
		path,
		[]byte(`{
			"go_memory_limit": 134217728
		}`),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.GoMemoryLimit != limit {
		t.Fatalf(
			"GoMemoryLimit=%d want=%d",
			cfg.GoMemoryLimit,
			limit,
		)
	}
}

func TestGoMemoryLimitEnvironmentOverride(
	t *testing.T,
) {
	cfg := Default()
	cfg.GoMemoryLimit = 64 * 1024 * 1024

	t.Setenv(
		"SNUGKV_GO_MEMORY_LIMIT",
		"134217728",
	)

	if err := cfg.ApplyEnv(); err != nil {
		t.Fatal(err)
	}

	if got := cfg.GoMemoryLimit; got != 128*1024*1024 {
		t.Fatalf(
			"GoMemoryLimit=%d",
			got,
		)
	}
}

func TestGoMemoryLimitRejectsNegative(
	t *testing.T,
) {
	cfg := Default()
	cfg.GoMemoryLimit = -1

	err := cfg.Validate()

	if err == nil {
		t.Fatal(
			"expected negative go_memory_limit to fail validation",
		)
	}
}

func TestGoMemoryLimitZeroIsValid(
	t *testing.T,
) {
	cfg := Default()
	cfg.GoMemoryLimit = 0

	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
