package server

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"snugkv/internal/config"
	"snugkv/internal/engine"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T, name string) *testCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// issue writes a certificate signed by the CA (server or client) and its key.
func (ca *testCA) issue(t *testing.T, dir, name string, serial int64, client bool) (certPath, keyPath string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	if client {
		tpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	} else {
		tpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	certPath, keyPath = filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	return certPath, keyPath
}

func freeTestAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// respDo sends one command and returns the first reply line.
func respDo(t *testing.T, c net.Conn, args ...string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("*" + itoa(len(args)) + "\r\n")
	for _, a := range args {
		b.WriteString("$" + itoa(len(a)) + "\r\n" + a + "\r\n")
	}
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte(b.String())); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(line)
}

func itoa(n int) string { return big.NewInt(int64(n)).String() }

func startTLSServer(t *testing.T, mutate func(*config.Config)) *TCPServer {
	t.Helper()
	c := config.Default()
	c.ListenAddr, c.AdminAddr, c.TLSListen = freeTestAddr(t), freeTestAddr(t), freeTestAddr(t)
	mutate(&c)
	s, err := ListenWithConfig(c, engine.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestTLSListenerWithRequirePass(t *testing.T) {
	dir := t.TempDir()
	ca := newTestCA(t, "test-ca")
	crt, key := ca.issue(t, dir, "server", 2, false)
	s := startTLSServer(t, func(c *config.Config) { c.TLSCert, c.TLSKey, c.RequirePass = crt, key, "s3cret-pass" })

	// Plain listener: password required
	plain, err := net.Dial("tcp", s.config.ListenAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if r := respDo(t, plain, "PING"); !strings.HasPrefix(r, "-NOAUTH") {
		t.Fatalf("PING without AUTH = %q", r)
	}
	if r := respDo(t, plain, "AUTH", "wrong"); !strings.HasPrefix(r, "-") {
		t.Fatalf("wrong password = %q", r)
	}
	if r := respDo(t, plain, "AUTH", "s3cret-pass"); r != "+OK" {
		t.Fatalf("AUTH = %q", r)
	}
	respDo(t, plain, "SET", "k", "v")

	// TLS listener: verified against the CA, same data, same password
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.pem)
	tc, err := tls.Dial("tcp", s.TLSAddr(), &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"})
	if err != nil {
		t.Fatalf("TLS dial: %v", err)
	}
	defer tc.Close()
	if r := respDo(t, tc, "PING"); !strings.HasPrefix(r, "-NOAUTH") {
		t.Fatalf("TLS PING without AUTH = %q", r)
	}
	respDo(t, tc, "AUTH", "s3cret-pass")
	if r := respDo(t, tc, "GET", "k"); r != "$1" {
		t.Fatalf("GET over TLS = %q", r)
	}
	if v := tc.ConnectionState().Version; v < tls.VersionTLS12 {
		t.Fatalf("TLS version %x", v)
	}

	// A client that does not trust the CA, or plain bytes on the TLS port, get nothing
	other := newTestCA(t, "other-ca")
	op := x509.NewCertPool()
	op.AppendCertsFromPEM(other.pem)
	if c, err := tls.Dial("tcp", s.TLSAddr(), &tls.Config{RootCAs: op, ServerName: "127.0.0.1"}); err == nil {
		c.Close()
		t.Fatal("a client with another CA must not connect")
	}
	if _, err := tls.Dial("tcp", s.TLSAddr(), &tls.Config{MaxVersion: tls.VersionTLS11, RootCAs: pool, ServerName: "127.0.0.1"}); err == nil {
		t.Fatal("TLS 1.1 must be refused")
	}
}

func TestTLSClientCertificates(t *testing.T) {
	dir := t.TempDir()
	ca := newTestCA(t, "test-ca")
	crt, key := ca.issue(t, dir, "server", 2, false)
	ccrt, ckey := ca.issue(t, dir, "client", 3, true)
	caPath := filepath.Join(dir, "ca.crt")
	os.WriteFile(caPath, ca.pem, 0o600)
	s := startTLSServer(t, func(c *config.Config) { c.TLSCert, c.TLSKey, c.TLSCACert, c.TLSAuthClients = crt, key, caPath, true })

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.pem)
	// Without a client certificate the server ends the connection
	if c, err := tls.Dial("tcp", s.TLSAddr(), &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"}); err == nil {
		c.SetDeadline(time.Now().Add(2 * time.Second))
		_, werr := c.Write([]byte("*1\r\n$4\r\nPING\r\n"))
		buf := make([]byte, 16)
		_, rerr := c.Read(buf)
		c.Close()
		if werr == nil && rerr == nil {
			t.Fatal("a client without a certificate must be refused")
		}
	}
	cert, err := tls.LoadX509KeyPair(ccrt, ckey)
	if err != nil {
		t.Fatal(err)
	}
	c, err := tls.Dial("tcp", s.TLSAddr(), &tls.Config{RootCAs: pool, ServerName: "127.0.0.1", Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("mTLS dial: %v", err)
	}
	defer c.Close()
	if r := respDo(t, c, "PING"); r != "+PONG" {
		t.Fatalf("PING with client certificate = %q", r)
	}
}

func TestTLSCertificateIsReloaded(t *testing.T) {
	old := certCheckEvery
	certCheckEvery = 0
	defer func() { certCheckEvery = old }()
	dir := t.TempDir()
	ca := newTestCA(t, "test-ca")
	crt, key := ca.issue(t, dir, "server", 2, false)
	s := startTLSServer(t, func(c *config.Config) { c.TLSCert, c.TLSKey = crt, key })
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.pem)
	serial := func() int64 {
		c, err := tls.Dial("tcp", s.TLSAddr(), &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		return c.ConnectionState().PeerCertificates[0].SerialNumber.Int64()
	}
	if n := serial(); n != 2 {
		t.Fatalf("serial %d", n)
	}
	// Renewal: new files under the same names
	ca.issue(t, dir, "server", 7, false)
	future := time.Now().Add(time.Minute)
	os.Chtimes(crt, future, future)
	os.Chtimes(key, future, future)
	if n := serial(); n != 7 {
		t.Fatalf("renewed certificate not served: serial %d", n)
	}
	// A broken file keeps the last good certificate
	os.WriteFile(crt, []byte("not a certificate"), 0o600)
	later := future.Add(time.Minute)
	os.Chtimes(crt, later, later)
	if n := serial(); n != 7 {
		t.Fatalf("after a broken renewal: serial %d", n)
	}
}

func TestTLSAndAuthConfigValidation(t *testing.T) {
	bad := []func(*config.Config){
		func(c *config.Config) { c.TLSListen = "127.0.0.1:0" }, // no cert
		func(c *config.Config) { c.TLSCert = "x" },             // cert without listener
		func(c *config.Config) {
			c.TLSListen, c.TLSCert, c.TLSKey, c.TLSAuthClients = "127.0.0.1:0", "a", "b", true
		}, // mTLS without CA
		func(c *config.Config) { c.RequirePass, c.ACLFile = "p", "/tmp/acl" },
	}
	for i, m := range bad {
		c := config.Default()
		m(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("case %d should be invalid", i)
		}
	}
}

func TestPasswordIsEnforcedBeforeAuth(t *testing.T) {
	s := startTLSServer(t, func(c *config.Config) { c.TLSListen = ""; c.RequirePass = "pw-0123456789" })
	c, err := net.Dial("tcp", s.config.ListenAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, cmd := range [][]string{{"PING"}, {"GET", "k"}, {"SET", "k", "v"}, {"KEYS", "*"}, {"EVAL", "return 1", "0"}, {"MULTI"}, {"HELLO", "3"}} {
		if r := respDo(t, c, cmd...); !strings.HasPrefix(r, "-NOAUTH") {
			t.Errorf("%v before AUTH = %q", cmd, r)
		}
	}
	if r := respDo(t, c, "HELLO", "2", "AUTH", "default", "pw-0123456789"); !strings.HasPrefix(r, "*") {
		t.Fatalf("HELLO with AUTH = %q", r)
	}
	if r := respDo(t, c, "PING"); r != "+PONG" {
		t.Fatalf("PING after HELLO AUTH = %q", r)
	}

	// Without a password nothing changes
	open := startTLSServer(t, func(c *config.Config) { c.TLSListen = "" })
	o, _ := net.Dial("tcp", open.config.ListenAddr)
	defer o.Close()
	if r := respDo(t, o, "PING"); r != "+PONG" {
		t.Fatalf("no password: PING = %q", r)
	}
}
