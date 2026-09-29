package server

import (
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestClusterRebalanceApplyOnceAuthenticatesControlAndMigrate(t *testing.T) {
	sourceTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer sourceTCP.Close()

	targetTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer targetTCP.Close()

	sourceAddr := sourceTCP.listener.Addr().String()
	targetAddr := targetTCP.listener.Addr().String()
	ranges := map[string]string{
		"0-9999":      sourceAddr,
		"10000-16383": targetAddr,
	}
	if err := sourceTCP.server.configureClusterSlots(true, sourceAddr, ranges); err != nil {
		t.Fatal(err)
	}
	if err := targetTCP.server.configureClusterSlots(true, targetAddr, ranges); err != nil {
		t.Fatal(err)
	}

	if err := targetTCP.server.acl.SetUser("default", []string{
		"reset",
		"on",
		">cluster-secret",
		"allkeys",
		"allchannels",
		"+@all",
	}); err != nil {
		t.Fatal(err)
	}
	sourceTCP.server.replicationMasterAuth = "cluster-secret"

	state := sourceTCP.server.clusterStateSnapshot()
	moves, err := planClusterRebalance(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) == 0 {
		t.Fatal("expected rebalance moves")
	}

	slot := moves[0].Start
	key := findClusterTestKeyForSlot(slot)
	if key == "" {
		t.Fatalf("failed to find key for slot %d", slot)
	}
	if _, err := sourceTCP.server.execute([][]byte{
		[]byte("SET"), []byte(key), []byte("value"),
	}); err != nil {
		t.Fatalf("seed source key: %v", err)
	}

	planID := clusterRebalancePlanID(state, moves)
	got, err := sourceTCP.server.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("APPLY"),
		[]byte(planID), []byte("ONCE"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "moved") {
		t.Fatalf("unexpected response: %q", got)
	}

	if sourceTCP.server.store.Exists([]string{key}) != 0 {
		t.Fatalf("source still contains %q", key)
	}
	value, found, wrongType := targetTCP.server.store.GetString(key)
	if wrongType || !found || string(value) != "value" {
		t.Fatalf("target value=%q found=%v wrongType=%v", value, found, wrongType)
	}
}

func TestClusterControlAuthenticationFailureIsExplicit(t *testing.T) {
	sourceTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer sourceTCP.Close()

	targetTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer targetTCP.Close()

	if err := targetTCP.server.acl.SetUser("default", []string{
		"reset",
		"on",
		">correct-secret",
		"allkeys",
		"allchannels",
		"+@all",
	}); err != nil {
		t.Fatal(err)
	}
	sourceTCP.server.replicationMasterAuth = "wrong-secret"

	err = sourceTCP.server.sendClusterControlCommand(
		targetTCP.listener.Addr().String(),
		"PING",
	)
	if err == nil || !strings.Contains(err.Error(), "rebalance target authentication failed") {
		t.Fatalf("err=%v", err)
	}
}

func TestRebalanceMigrateArgsUsesConfiguredCredentials(t *testing.T) {
	s := New(engine.New())

	args := s.rebalanceMigrateArgs("127.0.0.1", "7001", "key")
	if len(args) != 6 {
		t.Fatalf("unauthenticated args=%d want=6", len(args))
	}

	s.replicationMasterAuth = "secret"
	args = s.rebalanceMigrateArgs("127.0.0.1", "7001", "key")
	if got := string(args[6]); got != "AUTH" || string(args[7]) != "secret" {
		t.Fatalf("AUTH args=%q", args)
	}

	s.replicationMasterUser = "cluster"
	args = s.rebalanceMigrateArgs("127.0.0.1", "7001", "key")
	if got := string(args[6]); got != "AUTH2" ||
		string(args[7]) != "cluster" ||
		string(args[8]) != "secret" {
		t.Fatalf("AUTH2 args=%q", args)
	}
}


func TestInternalClusterControlRejectsPublicClient(t *testing.T) {
	targetTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer targetTCP.Close()

	targetAddr := targetTCP.listener.Addr().String()
	if err := targetTCP.server.configureClusterSlots(true, targetAddr, map[string]string{
		"0-16383": targetAddr,
	}); err != nil {
		t.Fatal(err)
	}
	targetTCP.server.clusterControlAuth = "control-secret"
	digest := clusterOwnershipDigest(targetTCP.server.clusterStateSnapshot())

	conn, reader := dialClusterClient(t, targetAddr)
	writeClusterRESPCommand(t, conn, "CLUSTER", "MEMBERSHIP", "CHECK", targetAddr, digest)
	if got := readRESPLine(t, reader); got != "-NOPERM internal cluster control authentication required\r\n" {
		t.Fatalf("public internal CLUSTER command=%q", got)
	}

	writeClusterRESPCommand(t, conn, "SNUG.FAILOVER", "STATE")
	if got := readRESPLine(t, reader); got != "-NOPERM internal cluster control authentication required\r\n" {
		t.Fatalf("public failover peer RPC=%q", got)
	}
}

func TestInternalClusterControlHandshakeAllowsPrivateRPC(t *testing.T) {
	targetTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer targetTCP.Close()

	targetAddr := targetTCP.listener.Addr().String()
	if err := targetTCP.server.configureClusterSlots(true, targetAddr, map[string]string{
		"0-16383": targetAddr,
	}); err != nil {
		t.Fatal(err)
	}
	targetTCP.server.clusterControlAuth = "control-secret"
	digest := clusterOwnershipDigest(targetTCP.server.clusterStateSnapshot())

	conn, reader := dialClusterClient(t, targetAddr)
	writeClusterRESPCommand(t, conn, "SNUG.INTERNAL", "AUTH", "control-secret")
	if got := readRESPLine(t, reader); got != "+OK\r\n" {
		t.Fatalf("internal auth=%q", got)
	}

	writeClusterRESPCommand(t, conn, "CLUSTER", "MEMBERSHIP", "CHECK", targetAddr, digest)
	if got := readRESPLine(t, reader); got != "+OK\r\n" {
		t.Fatalf("private cluster RPC=%q", got)
	}
}

func TestInternalClusterControlWrongSecretAndResetClearElevation(t *testing.T) {
	targetTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer targetTCP.Close()

	targetAddr := targetTCP.listener.Addr().String()
	if err := targetTCP.server.configureClusterSlots(true, targetAddr, map[string]string{
		"0-16383": targetAddr,
	}); err != nil {
		t.Fatal(err)
	}
	targetTCP.server.clusterControlAuth = "control-secret"
	digest := clusterOwnershipDigest(targetTCP.server.clusterStateSnapshot())

	conn, reader := dialClusterClient(t, targetAddr)
	writeClusterRESPCommand(t, conn, "SNUG.INTERNAL", "AUTH", "wrong-secret")
	if got := readRESPLine(t, reader); got != "-WRONGPASS invalid internal cluster control credential\r\n" {
		t.Fatalf("wrong internal auth=%q", got)
	}

	writeClusterRESPCommand(t, conn, "SNUG.INTERNAL", "AUTH", "control-secret")
	if got := readRESPLine(t, reader); got != "+OK\r\n" {
		t.Fatalf("internal auth=%q", got)
	}
	writeClusterRESPCommand(t, conn, "RESET")
	if got := readRESPLine(t, reader); got != "+RESET\r\n" {
		t.Fatalf("RESET=%q", got)
	}
	writeClusterRESPCommand(t, conn, "CLUSTER", "MEMBERSHIP", "CHECK", targetAddr, digest)
	if got := readRESPLine(t, reader); got != "-NOPERM internal cluster control authentication required\r\n" {
		t.Fatalf("internal elevation survived RESET: %q", got)
	}
}

func TestClusterControlSenderPerformsInternalHandshake(t *testing.T) {
	sourceTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer sourceTCP.Close()
	targetTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer targetTCP.Close()

	targetAddr := targetTCP.listener.Addr().String()
	if err := targetTCP.server.configureClusterSlots(true, targetAddr, map[string]string{
		"0-16383": targetAddr,
	}); err != nil {
		t.Fatal(err)
	}
	targetTCP.server.clusterControlAuth = "control-secret"
	sourceTCP.server.clusterControlAuth = "control-secret"
	digest := clusterOwnershipDigest(targetTCP.server.clusterStateSnapshot())

	if err := sourceTCP.server.sendClusterControlCommand(
		targetAddr,
		"CLUSTER", "MEMBERSHIP", "CHECK", targetAddr, digest,
	); err != nil {
		t.Fatalf("internal control sender failed: %v", err)
	}

	sourceTCP.server.clusterControlAuth = "wrong-secret"
	err = sourceTCP.server.sendClusterControlCommand(
		targetAddr,
		"CLUSTER", "MEMBERSHIP", "CHECK", targetAddr, digest,
	)
	if err == nil || !strings.Contains(err.Error(), "internal cluster control authentication failed") {
		t.Fatalf("wrong internal control secret err=%v", err)
	}
}
