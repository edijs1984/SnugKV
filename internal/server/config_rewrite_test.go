package server

import (
	"bytes"
	"errors"
	"testing"
)

func TestConfigRewriteSuccess(t *testing.T) {
	s := newConfigTestServer(t)

	called := false

	s.configRewrite = func() error {
		called = true
		return nil
	}

	reply, err := s.executeConfig(
		[][]byte{
			[]byte("CONFIG"),
			[]byte("REWRITE"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if !called {
		t.Fatal(
			"CONFIG REWRITE callback not called",
		)
	}

	if !bytes.Equal(
		reply,
		[]byte("+OK\r\n"),
	) {
		t.Fatalf(
			"unexpected reply: %q",
			reply,
		)
	}
}

func TestConfigRewriteFailure(t *testing.T) {
	s := newConfigTestServer(t)

	s.configRewrite = func() error {
		return errors.New("disk full")
	}

	_, err := s.executeConfig(
		[][]byte{
			[]byte("CONFIG"),
			[]byte("REWRITE"),
		},
	)

	if err == nil ||
		err.Error() !=
			"ERR Rewriting config file: disk full" {

		t.Fatalf(
			"unexpected error: %v",
			err,
		)
	}
}
