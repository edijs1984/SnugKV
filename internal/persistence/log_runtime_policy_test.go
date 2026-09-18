package persistence

import (
	"path/filepath"
	"testing"
)

func TestLogRuntimeFsyncPolicy(t *testing.T) {
	path := filepath.Join(
		t.TempDir(),
		"runtime-policy.aof",
	)

	log, err := Open(path, "no")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	if got := log.Policy(); got != "no" {
		t.Fatalf(
			"initial policy=%q want=no",
			got,
		)
	}

	for _, policy := range []string{
		"everysec",
		"always",
		"no",
	} {
		if err := log.SetPolicy(policy); err != nil {
			t.Fatalf(
				"SetPolicy(%q): %v",
				policy,
				err,
			)
		}

		if got := log.Policy(); got != policy {
			t.Fatalf(
				"policy=%q want=%q",
				got,
				policy,
			)
		}

		if err := log.Append(
			[]Record{
				{
					Key:   []byte("key"),
					Value: []byte(policy),
				},
			},
		); err != nil {
			t.Fatalf(
				"Append under policy %q: %v",
				policy,
				err,
			)
		}
	}
}

func TestLogRejectsInvalidRuntimeFsyncPolicy(
	t *testing.T,
) {
	path := filepath.Join(
		t.TempDir(),
		"invalid-policy.aof",
	)

	log, err := Open(path, "everysec")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	if err := log.SetPolicy("sometimes"); err == nil {
		t.Fatal(
			"expected invalid fsync policy error",
		)
	}

	if got := log.Policy(); got != "everysec" {
		t.Fatalf(
			"policy changed after invalid SET: %q",
			got,
		)
	}
}
