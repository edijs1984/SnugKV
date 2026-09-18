package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRewritePersistsEffectiveConfig(
	t *testing.T,
) {
	path := filepath.Join(
		t.TempDir(),
		"snugkv.json",
	)

	initial := Default()
	initial.SourcePath = path

	if err := Rewrite(initial); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.SourcePath != path {
		t.Fatalf(
			"SourcePath=%q want=%q",
			loaded.SourcePath,
			path,
		)
	}

	loaded.MaxMemory = 64 << 20
	loaded.EvictionPolicy = "allkeys-lru"
	loaded.MaxConnections = 1234
	loaded.Fsync = "always"

	if err := Rewrite(loaded); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if reloaded.MaxMemory != 64<<20 {
		t.Fatalf(
			"MaxMemory=%d",
			reloaded.MaxMemory,
		)
	}

	if reloaded.EvictionPolicy != "allkeys-lru" {
		t.Fatalf(
			"EvictionPolicy=%q",
			reloaded.EvictionPolicy,
		)
	}

	if reloaded.MaxConnections != 1234 {
		t.Fatalf(
			"MaxConnections=%d",
			reloaded.MaxConnections,
		)
	}

	if reloaded.Fsync != "always" {
		t.Fatalf(
			"Fsync=%q",
			reloaded.Fsync,
		)
	}
}

func TestRewriteWithoutSourcePath(t *testing.T) {
	cfg := Default()

	err := Rewrite(cfg)

	if err == nil ||
		err.Error() !=
			"The server is running without a config file" {

		t.Fatalf(
			"unexpected error: %v",
			err,
		)
	}
}

func TestRewritePreservesFileMode(t *testing.T) {
	path := filepath.Join(
		t.TempDir(),
		"snugkv.json",
	)

	if err := os.WriteFile(
		path,
		[]byte("{}\n"),
		0640,
	); err != nil {
		t.Fatal(err)
	}

	cfg := Default()
	cfg.SourcePath = path

	if err := Rewrite(cfg); err != nil {
		t.Fatal(err)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if got := st.Mode().Perm(); got != 0640 {
		t.Fatalf(
			"mode=%o want=640",
			got,
		)
	}
}
