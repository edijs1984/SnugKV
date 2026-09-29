package server

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func findTwoClusterTestKeysForSlot(slot int) (string, string) {
	first := ""
	for i := 0; i < 2000000; i++ {
		key := fmt.Sprintf("rebalance-recover-key-%d", i)
		if clusterKeySlot([]byte(key)) != slot {
			continue
		}
		if first == "" {
			first = key
			continue
		}
		return first, key
	}
	return "", ""
}

func TestClusterRebalanceRecoverPlanShowsSourceAndTargetActions(t *testing.T) {
	source := New(engine.New())
	target := New(engine.New())
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	ranges := map[string]string{
		"0-9999":      a,
		"10000-16383": b,
	}
	if err := source.configureClusterSlots(true, a, ranges); err != nil {
		t.Fatal(err)
	}
	if err := target.configureClusterSlots(true, b, ranges); err != nil {
		t.Fatal(err)
	}

	source.clusterMu.Lock()
	source.clusterSlotMigrating[9999] = b
	source.clusterTopologyEpoch = 7
	source.clusterMu.Unlock()

	target.clusterMu.Lock()
	target.clusterSlotImporting[9999] = a
	target.clusterTopologyEpoch = 9
	target.clusterMu.Unlock()

	got, err := source.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("RECOVER"), []byte("PLAN"),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, token := range []string{
		"recovery_needed", "epoch", ":7\r\n", "slot", ":9999\r\n",
		"role", "source", "peer", b, "owner", a, "action", "resume",
	} {
		if !strings.Contains(text, token) {
			t.Fatalf("source recovery plan missing %q: %q", token, text)
		}
	}

	got, err = target.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("RECOVER"), []byte("PLAN"),
	})
	if err != nil {
		t.Fatal(err)
	}
	text = string(got)
	for _, token := range []string{
		"recovery_needed", "epoch", ":9\r\n", "slot", ":9999\r\n",
		"role", "target", "peer", a, "owner", a, "action", "wait_for_source",
	} {
		if !strings.Contains(text, token) {
			t.Fatalf("target recovery plan missing %q: %q", token, text)
		}
	}
}

func TestClusterRebalanceRecoverResumeCompletesSplitSlot(t *testing.T) {
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

	slot := 9999
	sourceKey, alreadyMovedKey := findTwoClusterTestKeysForSlot(slot)
	if sourceKey == "" || alreadyMovedKey == "" {
		t.Fatalf("failed to find two keys for slot %d", slot)
	}

	if _, err := sourceTCP.server.execute([][]byte{
		[]byte("SET"), []byte(sourceKey), []byte("source-value"),
	}); err != nil {
		t.Fatalf("seed source key: %v", err)
	}
	if err := targetTCP.server.store.SetPlain(alreadyMovedKey, []byte("target-value")); err != nil {
		t.Fatalf("seed already-moved target key: %v", err)
	}

	if _, err := targetTCP.server.executeClusterSetSlot([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"), []byte("9999"),
		[]byte("IMPORTING"), []byte(clusterNodeID(sourceAddr)),
	}); err != nil {
		t.Fatalf("target importing: %v", err)
	}
	if _, err := sourceTCP.server.executeClusterSetSlot([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"), []byte("9999"),
		[]byte("MIGRATING"), []byte(clusterNodeID(targetAddr)),
	}); err != nil {
		t.Fatalf("source migrating: %v", err)
	}

	got, err := sourceTCP.server.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("RECOVER"),
		[]byte("RESUME"), []byte("9999"),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, token := range []string{"status", "recovered", "slot", ":9999\r\n", "target_addr", targetAddr, "keys_moved", ":1\r\n"} {
		if !strings.Contains(text, token) {
			t.Fatalf("resume response missing %q: %q", token, text)
		}
	}

	sourceState := sourceTCP.server.clusterStateSnapshot()
	targetState := targetTCP.server.clusterStateSnapshot()
	if sourceState.owners[slot] != targetAddr || targetState.owners[slot] != targetAddr {
		t.Fatalf("ownership not converged: source=%q target=%q want=%q",
			sourceState.owners[slot], targetState.owners[slot], targetAddr)
	}
	if sourceState.migrating[slot] != "" || sourceState.importing[slot] != "" ||
		targetState.migrating[slot] != "" || targetState.importing[slot] != "" {
		t.Fatal("transition markers not cleared after recovery")
	}

	if sourceTCP.server.store.Exists([]string{sourceKey, alreadyMovedKey}) != 0 {
		t.Fatal("source retained key data after recovery")
	}
	for key, want := range map[string]string{
		sourceKey:       "source-value",
		alreadyMovedKey: "target-value",
	} {
		value, found, wrongType := targetTCP.server.store.GetString(key)
		if wrongType || !found || string(value) != want {
			t.Fatalf("target key %q value=%q found=%v wrongType=%v want=%q", key, value, found, wrongType, want)
		}
	}
}

func TestClusterRebalanceRecoverResumeRejectsNonMigratingSlot(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-9999":      a,
		"10000-16383": b,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := s.execute([][]byte{
		[]byte("CLUSTER"), []byte("REBALANCE"), []byte("RECOVER"),
		[]byte("RESUME"), []byte("9999"),
	})
	want := "ERR REBALANCE RECOVER RESUME requires a locally migrating slot"
	if err == nil || err.Error() != want {
		t.Fatalf("err=%v want=%q", err, want)
	}
}


func TestClusterRebalanceRecoverResumeReassertsTargetImporting(t *testing.T) {
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

	slot := 9999
	key := findClusterTestKeyForSlot(slot)
	if key == "" {
		t.Fatalf("failed to find key for slot %d", slot)
	}
	if _, err := sourceTCP.server.execute([][]byte{
		[]byte("SET"), []byte(key), []byte("resume-value"),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := sourceTCP.server.executeClusterSetSlot([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"), []byte(strconv.Itoa(slot)),
		[]byte("MIGRATING"), []byte(clusterNodeID(targetAddr)),
	}); err != nil {
		t.Fatal(err)
	}

	// Model target restart/state loss: the source still has the durable
	// MIGRATING marker but target-side IMPORTING is absent.
	targetTCP.server.clusterMu.Lock()
	targetTCP.server.clusterSlotImporting[slot] = ""
	targetTCP.server.clusterSlotMigrating[slot] = ""
	targetTCP.server.clusterMu.Unlock()

	state := sourceTCP.server.clusterStateSnapshot()
	moved, err := sourceTCP.server.resumeRebalanceSlot(state, slot)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 1 {
		t.Fatalf("moved=%d want=1", moved)
	}

	sourceFinal := sourceTCP.server.clusterStateSnapshot()
	targetFinal := targetTCP.server.clusterStateSnapshot()
	if sourceFinal.owners[slot] != targetAddr || targetFinal.owners[slot] != targetAddr {
		t.Fatalf("ownership not converged: source=%q target=%q",
			sourceFinal.owners[slot], targetFinal.owners[slot])
	}
	if sourceFinal.migrating[slot] != "" || targetFinal.importing[slot] != "" {
		t.Fatalf("transition markers remain: source=%q target=%q",
			sourceFinal.migrating[slot], targetFinal.importing[slot])
	}
	value, found, wrongType := targetTCP.server.store.GetString(key)
	if wrongType || !found || string(value) != "resume-value" {
		t.Fatalf("target value=%q found=%v wrongType=%v", value, found, wrongType)
	}
}
