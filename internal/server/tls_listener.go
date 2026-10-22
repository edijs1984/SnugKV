package server

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"snugkv/internal/config"
)

// certReloader serves the certificate from disk and reloads it when the files
// change, so renewed certificates (Let's Encrypt, an internal CA) are picked
// up without a restart. A broken new file keeps the last good certificate.
// certCheckEvery is how often the files are checked for a renewed certificate.
var certCheckEvery = 10 * time.Second

type certReloader struct {
	certPath, keyPath string

	mu      sync.Mutex
	cert    *tls.Certificate
	modCert time.Time
	modKey  time.Time
	checked time.Time
}

func newCertReloader(certPath, keyPath string) (*certReloader, error) {
	r := &certReloader{certPath: certPath, keyPath: keyPath}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *certReloader) load() error {
	ci, err := os.Stat(r.certPath)
	if err != nil {
		return err
	}
	ki, err := os.Stat(r.keyPath)
	if err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(r.certPath, r.keyPath)
	if err != nil {
		return fmt.Errorf("tls certificate: %w", err)
	}
	r.cert, r.modCert, r.modKey = &cert, ci.ModTime(), ki.ModTime()
	return nil
}

func (r *certReloader) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.checked) > certCheckEvery {
		r.checked = time.Now()
		ci, err1 := os.Stat(r.certPath)
		ki, err2 := os.Stat(r.keyPath)
		if err1 == nil && err2 == nil && (!ci.ModTime().Equal(r.modCert) || !ki.ModTime().Equal(r.modKey)) {
			_ = r.load() // keep serving the old one if the new pair is broken or half-written
		}
	}
	return r.cert, nil
}

// tlsConfigFor builds the listener's TLS settings: TLS 1.2+, and client
// certificates from tls_ca_cert (required with tls_auth_clients, otherwise
// checked when a client sends one).
func tlsConfigFor(c config.Config) (*tls.Config, error) {
	rel, err := newCertReloader(c.TLSCert, c.TLSKey)
	if err != nil {
		return nil, err
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: rel.get}
	if c.TLSCACert != "" {
		pem, err := os.ReadFile(c.TLSCACert)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("tls_ca_cert holds no PEM certificate")
		}
		tc.ClientCAs = pool
		tc.ClientAuth = tls.VerifyClientCertIfGiven
		if c.TLSAuthClients {
			tc.ClientAuth = tls.RequireAndVerifyClientCert
		}
	}
	return tc, nil
}

// openTLS starts the TLS listener next to the plain one. Its connections are
// handled exactly like plain ones (same server, same limits); the plain TCP
// socket underneath is used to notice disconnects of blocked clients.
func (s *TCPServer) openTLS(c config.Config) error {
	tc, err := tlsConfigFor(c)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", c.TLSListen)
	if err != nil {
		return err
	}
	s.tlsListener = tls.NewListener(ln, tc)
	s.wg.Add(1)
	go s.serveListener(s.tlsListener)
	return nil
}

// TLSAddr is the bound TLS address, or "" without a TLS listener.
func (s *TCPServer) TLSAddr() string {
	if s.tlsListener == nil {
		return ""
	}
	return s.tlsListener.Addr().String()
}
