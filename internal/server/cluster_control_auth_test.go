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
	want := []string{"MIGRATE", "127.0.0.1", "7001", "", "0", "5000", "KEYS", "key"}
	if len(args) != len(want) {
		t.Fatalf("unauthenticated args=%d want=%d args=%q", len(args), len(want), args)
	}
	for i := range want {
		if string(args[i]) != want[i] {
			t.Fatalf("unauthenticated arg[%d]=%q want=%q args=%q", i, args[i], want[i], args)
		}
	}

	s.replicationMasterAuth = "secret"
	args = s.rebalanceMigrateArgs("127.0.0.1", "7001", "key")
	if len(args) != 10 ||
		string(args[6]) != "AUTH" ||
		string(args[7]) != "secret" ||
		string(args[8]) != "KEYS" ||
		string(args[9]) != "key" {
		t.Fatalf("AUTH args=%q", args)
	}

	s.replicationMasterUser = "cluster"
	args = s.rebalanceMigrateArgs("127.0.0.1", "7001", "key")
	if len(args) != 11 ||
		string(args[6]) != "AUTH2" ||
		string(args[7]) != "cluster" ||
		string(args[8]) != "secret" ||
		string(args[9]) != "KEYS" ||
		string(args[10]) != "key" {
		t.Fatalf("AUTH2 args=%q", args)
	}
}
