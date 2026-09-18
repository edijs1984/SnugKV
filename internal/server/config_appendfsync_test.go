package server

import (
	"bytes"
	"strings"
	"testing"
)

func TestConfigSetAppendFsync(t *testing.T) {
	s := newConfigTestServer(t)

	s.configSetAppendFsync = func(
		policy string,
	) error {
		s.configMu.Lock()
		s.configAppendFsync = policy
		s.configMu.Unlock()

		return nil
	}

	for _, policy := range []string{
		"always",
		"no",
		"everysec",
	} {
		if err := s.configSet(
			"appendfsync",
			policy,
		); err != nil {
			t.Fatalf(
				"CONFIG SET appendfsync %s: %v",
				policy,
				err,
			)
		}

		reply := s.configGet(
			[][]byte{
				[]byte("appendfsync"),
			},
		)

		if !bytes.Contains(
			reply,
			[]byte(policy),
		) {
			t.Fatalf(
				"policy %q not reflected in %q",
				policy,
				reply,
			)
		}
	}
}

func TestConfigSetAppendFsyncRejectsInvalid(
	t *testing.T,
) {
	s := newConfigTestServer(t)

	s.configSetAppendFsync = func(
		policy string,
	) error {
		return nil
	}

	err := s.configSet(
		"appendfsync",
		"sometimes",
	)

	if err == nil ||
		!strings.Contains(
			err.Error(),
			"always, everysec, no",
		) {

		t.Fatalf(
			"unexpected error: %v",
			err,
		)
	}
}

func TestConfigSetMultipleIncludingAppendFsync(
	t *testing.T,
) {
	s := newConfigTestServer(t)

	s.configSetAppendFsync = func(
		policy string,
	) error {
		s.configMu.Lock()
		s.configAppendFsync = policy
		s.configMu.Unlock()

		return nil
	}

	reply, err := s.executeConfig(
		[][]byte{
			[]byte("CONFIG"),
			[]byte("SET"),
			[]byte("maxmemory"),
			[]byte("16mb"),
			[]byte("appendfsync"),
			[]byte("always"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if string(reply) != "+OK\r\n" {
		t.Fatalf(
			"unexpected reply: %q",
			reply,
		)
	}

	if got := s.store.MaxMemory(); got != 16<<20 {
		t.Fatalf(
			"maxmemory=%d want=%d",
			got,
			uint64(16<<20),
		)
	}

	configReply := s.configGet(
		[][]byte{
			[]byte("appendfsync"),
		},
	)

	if !bytes.Contains(
		configReply,
		[]byte("always"),
	) {
		t.Fatalf(
			"appendfsync not updated: %q",
			configReply,
		)
	}
}
