// Package secrets is an optional secrets / environment-variable manager on
// top of SnugKV. Values are encrypted with AES-256-GCM before they are
// stored, grouped by project and environment ("airsig/prod"), versioned, and
// every read and change is written to an audit stream. Services read them
// with scoped read tokens; people manage them through the API (and the
// console's Secrets page).
//
// The module is off by default. Like the webhook module it talks to the
// server over its own connection, so the data is journaled and replicated
// like any other keys; it needs no special access to the engine.
package secrets

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

// Options configure the module.
type Options struct {
	// KVAddr is the SnugKV address the module connects to (the server's own listen address).
	KVAddr, KVUser, KVPass string
	// APIListen serves the API, for example 127.0.0.1:8092.
	APIListen string
	// APIToken is the admin bearer token. It is required unless APIListen is a loopback address.
	APIToken string
	// Key encrypts the stored values (16+ characters). Required.
	Key string
	// Logf receives log lines. Default: discard.
	Logf func(format string, args ...any)
}

// Manager runs the module.
type Manager struct {
	opts   Options
	kv     *kvClient
	sealer *sealer
	srv    *http.Server
	addr   string
}

func (m *Manager) logf(format string, args ...any) {
	if m.opts.Logf != nil {
		m.opts.Logf(format, args...)
	}
}

// Start launches the module. Close stops it.
func Start(opts Options) (*Manager, error) {
	if opts.APIListen == "" {
		return nil, errors.New("secrets: no API address")
	}
	if opts.APIToken == "" && !isLoopback(opts.APIListen) {
		return nil, errors.New("secrets: an API on a non-loopback address needs -secrets-api-token")
	}
	if opts.Key == "" {
		return nil, errors.New("secrets: -secrets-key (or SNUGKV_SECRETS_KEY) is required")
	}
	if strings.HasPrefix(opts.APIToken, "sks_") {
		return nil, errors.New("secrets: the admin token must not look like a read token (sks_...)")
	}
	sl, err := newSealer(opts.Key)
	if err != nil {
		return nil, err
	}
	m := &Manager{opts: opts, kv: newKVClient(opts.KVAddr, opts.KVUser, opts.KVPass, 8, 5*time.Second), sealer: sl}
	if err := m.checkKeyWithRetry(); err != nil {
		m.kv.close()
		return nil, err
	}
	ln, err := net.Listen("tcp", opts.APIListen)
	if err != nil {
		m.kv.close()
		return nil, err
	}
	m.addr = ln.Addr().String()
	m.srv = &http.Server{Handler: m.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	go m.srv.Serve(ln)
	return m, nil
}

// checkKeyWithRetry gives the server a moment when both start together.
func (m *Manager) checkKeyWithRetry() error {
	var err error
	for i := 0; i < 20; i++ {
		if err = m.checkKey(); err == nil {
			return nil
		}
		var se serverError
		if errors.As(err, &se) || strings.Contains(err.Error(), "secrets key") {
			if strings.HasPrefix(string(se), "READONLY") {
				return nil // a replica: the primary holds the check value
			}
			return err
		}
		time.Sleep(250 * time.Millisecond)
	}
	return err
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Addr is the bound API address.
func (m *Manager) Addr() string { return m.addr }

// Close stops the API.
func (m *Manager) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	m.srv.Shutdown(ctx)
	cancel()
	m.kv.close()
}
