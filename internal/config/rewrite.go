package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Rewrite atomically persists the effective configuration to the original
// strict JSON configuration file.
//
// SourcePath itself is runtime metadata and is excluded from JSON.
func Rewrite(c Config) error {
	if c.SourcePath == "" {
		return errors.New(
			"The server is running without a config file",
		)
	}

	if err := c.Validate(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(
		c,
		"",
		"  ",
	)
	if err != nil {
		return err
	}

	data = append(data, '\n')

	path := c.SourcePath
	dir := filepath.Dir(path)

	mode := os.FileMode(0600)

	if st, statErr := os.Stat(path); statErr == nil {
		mode = st.Mode().Perm()
	} else if !os.IsNotExist(statErr) {
		return statErr
	}

	tmp, err := os.CreateTemp(
		dir,
		".snugkv-config-*",
	)
	if err != nil {
		return err
	}

	tmpName := tmp.Name()

	defer os.Remove(tmpName)

	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	if err := os.Rename(
		tmpName,
		path,
	); err != nil {
		return err
	}

	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer dirFile.Close()

	return dirFile.Sync()
}
