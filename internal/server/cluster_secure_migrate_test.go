package server

import (
	"bufio"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"crypto/tls"
	"snugkv/internal/engine"
)

func writeTestClusterTLSCerts(t *testing.T) (caPath string, cert tls.Certificate) {
	t.Helper()
	dir := t.TempDir()

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil { t.Fatal(err) }
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{CommonName: "SnugKV Test CA"},
		NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour),
		IsCA: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil { t.Fatal(err) }
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	caPath = filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caPath, caPEM, 0600); err != nil { t.Fatal(err) }

	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil { t.Fatal(err) }
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject: pkix.Name{CommonName: "localhost"},
		DNSNames: []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil { t.Fatal(err) }

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey)})
	cert, err = tls.X509KeyPair(certPEM, keyPEM)
	if err != nil { t.Fatal(err) }
	return caPath, cert
}

func startTLSMigrateTarget(t *testing.T, cert tls.Certificate, replies []string, got chan<- [][][]byte) (host, port string) {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		MinVersion: tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	})
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = ln.Close() })

	addr := ln.Addr().(*net.TCPAddr)
	go func() {
		conn, err := ln.Accept()
		if err != nil { return }
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		r := bufio.NewReader(conn)
		commands := make([][][]byte, 0, len(replies))
		for _, reply := range replies {
			command, err := readTestRESPCommand(r)
			if err != nil { return }
			commands = append(commands, command)
			_, _ = io.WriteString(conn, reply)
		}
		got <- commands
	}()

	return "127.0.0.1", strconv.Itoa(addr.Port)
}

func TestClusterInternalMigrateUsesTLSAndAuth(t *testing.T) {
	caPath, cert := writeTestClusterTLSCerts(t)
	got := make(chan [][][]byte, 1)
	host, port := startTLSMigrateTarget(t, cert,
		[]string{
			"+OK\r\n", // AUTH
			"+OK\r\n", // SELECT
			"+OK\r\n", // RESTORE-ASKING
		},
		got,
	)
	targetAddr := net.JoinHostPort(host, port)
	local := "127.0.0.1:7000"

	s := New(engine.New())
	if err := s.configureClusterSlots(true, local, map[string]string{
		"0-16383": local,
	}); err != nil { t.Fatal(err) }
	s.clusterMu.Lock()
	s.clusterKnownNodes[targetAddr] = struct{}{}
	s.clusterMu.Unlock()

	s.replicationMasterTLS = true
	s.replicationMasterTLSCA = caPath
	s.replicationMasterTLSSNI = "localhost"
	s.replicationMasterAuth = "cluster-secret"

	key := findClusterTestKeyForSlot(1)
	if key == "" { t.Fatal("failed to find test key") }
	slot := clusterKeySlot([]byte(key))
	if _, err := s.execute([][]byte{[]byte("SET"), []byte(key), []byte("value")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.executeClusterSetSlot([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"), []byte(strconv.Itoa(slot)),
		[]byte("MIGRATING"), []byte(clusterNodeID(targetAddr)),
	}); err != nil { t.Fatal(err) }

	response, err := s.executeClusterMigrateDurableLocked(
		s.rebalanceMigrateArgs(host, port, key),
	)
	if err != nil || string(response) != "+OK\r\n" {
		t.Fatalf("secure cluster migrate response=%q err=%v", response, err)
	}
	if s.store.Exists([]string{key}) != 0 {
		t.Fatalf("source still contains migrated key %q", key)
	}

	select {
	case commands := <-got:
		if len(commands) != 3 {
			t.Fatalf("commands=%d want=3", len(commands))
		}
		if string(commands[0][0]) != "AUTH" || string(commands[0][1]) != "cluster-secret" {
			t.Fatalf("AUTH=%q", commands[0])
		}
		if string(commands[1][0]) != "SELECT" {
			t.Fatalf("SELECT=%q", commands[1])
		}
		if string(commands[2][0]) != "RESTORE-ASKING" || string(commands[2][1]) != key {
			t.Fatalf("RESTORE-ASKING=%q", commands[2])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("TLS migrate target did not receive commands")
	}
}

func TestPublicMigrateDoesNotUseClusterTLSSettings(t *testing.T) {
	caPath, cert := writeTestClusterTLSCerts(t)
	got := make(chan [][][]byte, 1)
	host, port := startTLSMigrateTarget(t, cert, []string{"+OK\r\n", "+OK\r\n"}, got)

	s := New(engine.New())
	s.replicationMasterTLS = true
	s.replicationMasterTLSCA = caPath
	s.replicationMasterTLSSNI = "localhost"

	if _, err := s.execute([][]byte{[]byte("SET"), []byte("public:migrate"), []byte("v")}); err != nil {
		t.Fatal(err)
	}
	_, err := s.execute([][]byte{
		[]byte("MIGRATE"), []byte(host), []byte(port), []byte("public:migrate"),
		[]byte("0"), []byte("250"),
	})
	if err == nil {
		t.Fatal("public MIGRATE unexpectedly inherited cluster TLS transport")
	}
	if s.store.Exists([]string{"public:migrate"}) != 1 {
		t.Fatal("failed public MIGRATE deleted source key")
	}
}
