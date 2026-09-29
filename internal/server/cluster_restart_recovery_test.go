package server

import (
	"path/filepath"
	"strconv"
	"testing"

	"snugkv/internal/engine"
)

func TestClusterRebalanceRecoverySurvivesSourceRestart(t *testing.T) {
	targetTCP, err := Listen("127.0.0.1:0", engine.New())
	if err != nil {
		t.Fatal(err)
	}
	defer targetTCP.Close()

	sourceAddr := "127.0.0.1:7000"
	targetAddr := targetTCP.listener.Addr().String()
	ranges := map[string]string{
		"0-8191":     sourceAddr,
		"8192-16383": targetAddr,
	}

	sourceStore := engine.New()
	source := New(sourceStore)
	if err := source.configureClusterSlots(true, sourceAddr, ranges); err != nil {
		t.Fatal(err)
	}
	if err := targetTCP.server.configureClusterSlots(true, targetAddr, ranges); err != nil {
		t.Fatal(err)
	}

	sidecarBase := filepath.Join(t.TempDir(), "appendonly.aof")
	sourceTCP := &TCPServer{server: source}
	if err := sourceTCP.ConfigureClusterPersistence(sidecarBase, ""); err != nil {
		t.Fatal(err)
	}
	defer clusterTopologyPersistencePaths.Delete(source)

	slot := 100
	key := findClusterTestKeyForSlot(slot)
	if key == "" {
		t.Fatalf("failed to find key for slot %d", slot)
	}
	if _, err := source.execute([][]byte{
		[]byte("SET"), []byte(key), []byte("restart-value"),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := targetTCP.server.executeClusterSetSlot([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"), []byte(strconv.Itoa(slot)),
		[]byte("IMPORTING"), []byte(clusterNodeID(sourceAddr)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.executeClusterSetSlot([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"), []byte(strconv.Itoa(slot)),
		[]byte("MIGRATING"), []byte(clusterNodeID(targetAddr)),
	}); err != nil {
		t.Fatal(err)
	}

	before := source.clusterStateSnapshot()
	if before.migrating[slot] != targetAddr {
		t.Fatalf("source transition not persisted before restart: %+v", before.migrating[slot])
	}

	// Rebuild the server around the same recovered dataset while loading the
	// persisted cluster control-plane sidecar, which models process restart of
	// the topology/recovery state.
	restarted := New(sourceStore)
	if err := restarted.configureClusterSlots(true, sourceAddr, ranges); err != nil {
		t.Fatal(err)
	}
	restartedTCP := &TCPServer{server: restarted}
	if err := restartedTCP.ConfigureClusterPersistence(sidecarBase, ""); err != nil {
		t.Fatal(err)
	}
	defer clusterTopologyPersistencePaths.Delete(restarted)

	recovered := restarted.clusterStateSnapshot()
	if recovered.migrating[slot] != targetAddr {
		t.Fatalf("restart lost migrating marker: got=%q want=%q", recovered.migrating[slot], targetAddr)
	}

	moved, err := restarted.resumeRebalanceSlot(recovered, slot)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 1 {
		t.Fatalf("keys moved=%d want=1", moved)
	}
	if restarted.store.Exists([]string{key}) != 0 {
		t.Fatalf("source still contains %q", key)
	}
	value, found, wrongType := targetTCP.server.store.GetString(key)
	if wrongType || !found || string(value) != "restart-value" {
		t.Fatalf("target value=%q found=%v wrongType=%v", value, found, wrongType)
	}

	final := restarted.clusterStateSnapshot()
	if final.owners[slot] != targetAddr || final.migrating[slot] != "" {
		t.Fatalf("source did not finalize recovered slot: owner=%q migrating=%q", final.owners[slot], final.migrating[slot])
	}
	targetFinal := targetTCP.server.clusterStateSnapshot()
	if targetFinal.owners[slot] != targetAddr || targetFinal.importing[slot] != "" {
		t.Fatalf("target did not finalize recovered slot: owner=%q importing=%q", targetFinal.owners[slot], targetFinal.importing[slot])
	}
}

func TestClusterRebalanceRecoveryRestartPreservesOwnershipDigest(t *testing.T) {
	sourceAddr := "127.0.0.1:7000"
	targetAddr := "127.0.0.1:7001"
	ranges := map[string]string{
		"0-8191":     sourceAddr,
		"8192-16383": targetAddr,
	}

	sidecarBase := filepath.Join(t.TempDir(), "appendonly.aof")
	source := New(engine.New())
	if err := source.configureClusterSlots(true, sourceAddr, ranges); err != nil {
		t.Fatal(err)
	}
	tcp := &TCPServer{server: source}
	if err := tcp.ConfigureClusterPersistence(sidecarBase, ""); err != nil {
		t.Fatal(err)
	}
	defer clusterTopologyPersistencePaths.Delete(source)

	if _, err := source.executeClusterSetSlot([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"), []byte("100"),
		[]byte("MIGRATING"), []byte(clusterNodeID(targetAddr)),
	}); err != nil {
		t.Fatal(err)
	}
	before := source.clusterStateSnapshot()
	beforeDigest := clusterOwnershipDigest(before)

	restarted := New(engine.New())
	if err := restarted.configureClusterSlots(true, sourceAddr, ranges); err != nil {
		t.Fatal(err)
	}
	restartedTCP := &TCPServer{server: restarted}
	if err := restartedTCP.ConfigureClusterPersistence(sidecarBase, ""); err != nil {
		t.Fatal(err)
	}
	defer clusterTopologyPersistencePaths.Delete(restarted)

	after := restarted.clusterStateSnapshot()
	if clusterOwnershipDigest(after) != beforeDigest {
		t.Fatalf("ownership digest changed across restart")
	}
	if after.migrating[100] != targetAddr {
		t.Fatalf("transition marker lost across restart: %q", after.migrating[100])
	}
}
