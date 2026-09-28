package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestClusterTopologyPersistenceRestoresRuntimeOwnershipAndEpoch(t *testing.T) {
	dir := t.TempDir()
	aofPath := filepath.Join(dir, "appendonly.aof")
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	ranges := map[string]string{
		"0-9999":      a,
		"10000-16383": b,
	}

	s := New(engine.New())
	if err := s.configureClusterSlots(true, a, ranges); err != nil {
		t.Fatal(err)
	}
	tcp := &TCPServer{server: s}
	if err := tcp.ConfigureClusterPersistence(aofPath, ""); err != nil {
		t.Fatal(err)
	}
	defer clusterTopologyPersistencePaths.Delete(s)

	slot := 9999
	if _, err := s.executeClusterSetSlot([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"), []byte("9999"),
		[]byte("NODE"), []byte(clusterNodeID(b)),
	}); err != nil {
		t.Fatal(err)
	}
	after := s.clusterStateSnapshot()
	if after.owners[slot] != b {
		t.Fatalf("owner[%d]=%q want=%q", slot, after.owners[slot], b)
	}
	if after.epoch != 1 {
		t.Fatalf("epoch=%d want=1", after.epoch)
	}

	if _, err := os.Stat(aofPath + ".cluster"); err != nil {
		t.Fatalf("cluster sidecar missing: %v", err)
	}

	restarted := New(engine.New())
	if err := restarted.configureClusterSlots(true, a, ranges); err != nil {
		t.Fatal(err)
	}
	restartedTCP := &TCPServer{server: restarted}
	if err := restartedTCP.ConfigureClusterPersistence(aofPath, ""); err != nil {
		t.Fatal(err)
	}
	defer clusterTopologyPersistencePaths.Delete(restarted)

	recovered := restarted.clusterStateSnapshot()
	if recovered.owners[slot] != b {
		t.Fatalf("recovered owner[%d]=%q want=%q", slot, recovered.owners[slot], b)
	}
	if recovered.epoch != 1 {
		t.Fatalf("recovered epoch=%d want=1", recovered.epoch)
	}

	info := string(restarted.clusterInfoReply())
	if !strings.Contains(info, "cluster_current_epoch:1\r\n") ||
		!strings.Contains(info, "cluster_my_epoch:1\r\n") {
		t.Fatalf("cluster info missing recovered epoch: %q", info)
	}
}

func TestClusterTopologyPersistenceRestoresTransitionState(t *testing.T) {
	dir := t.TempDir()
	snapshotPath := filepath.Join(dir, "snapshot.rdb")
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	ranges := map[string]string{
		"0-9999":      a,
		"10000-16383": b,
	}

	s := New(engine.New())
	if err := s.configureClusterSlots(true, a, ranges); err != nil {
		t.Fatal(err)
	}
	tcp := &TCPServer{server: s}
	if err := tcp.ConfigureClusterPersistence("", snapshotPath); err != nil {
		t.Fatal(err)
	}
	defer clusterTopologyPersistencePaths.Delete(s)

	if _, err := s.executeClusterSetSlot([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"), []byte("9999"),
		[]byte("MIGRATING"), []byte(clusterNodeID(b)),
	}); err != nil {
		t.Fatal(err)
	}
	if got := s.clusterStateSnapshot(); got.epoch != 1 || got.migrating[9999] != b {
		t.Fatalf("unexpected persisted source state: epoch=%d migrating=%q", got.epoch, got.migrating[9999])
	}

	restarted := New(engine.New())
	if err := restarted.configureClusterSlots(true, a, ranges); err != nil {
		t.Fatal(err)
	}
	restartedTCP := &TCPServer{server: restarted}
	if err := restartedTCP.ConfigureClusterPersistence("", snapshotPath); err != nil {
		t.Fatal(err)
	}
	defer clusterTopologyPersistencePaths.Delete(restarted)

	got := restarted.clusterStateSnapshot()
	if got.epoch != 1 || got.migrating[9999] != b {
		t.Fatalf("recovered epoch=%d migrating=%q want epoch=1 target=%q", got.epoch, got.migrating[9999], b)
	}
}

func TestClusterTopologyPersistenceFailureRollsBackMutationAndEpoch(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	s := New(engine.New())
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-9999":      a,
		"10000-16383": b,
	}); err != nil {
		t.Fatal(err)
	}
	clusterTopologyPersistencePaths.Store(s, filepath.Join(blocker, "topology.cluster"))
	defer clusterTopologyPersistencePaths.Delete(s)

	before := s.clusterStateSnapshot()
	_, err := s.executeClusterSetSlot([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"), []byte("9999"),
		[]byte("NODE"), []byte(clusterNodeID(b)),
	})
	if err == nil || !strings.Contains(err.Error(), "ERR cluster topology persistence failed") {
		t.Fatalf("err=%v", err)
	}

	after := s.clusterStateSnapshot()
	if after.epoch != before.epoch ||
		after.owners != before.owners ||
		after.migrating != before.migrating ||
		after.importing != before.importing {
		t.Fatal("failed persistence left mutated topology in memory")
	}
}

func TestClusterTopologyEpochDoesNotAdvanceOnNoop(t *testing.T) {
	s := New(engine.New())
	a := "127.0.0.1:7000"
	b := "127.0.0.1:7001"
	if err := s.configureClusterSlots(true, a, map[string]string{
		"0-9999":      a,
		"10000-16383": b,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.executeClusterSetSlot([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"), []byte("9999"), []byte("STABLE"),
	}); err != nil {
		t.Fatal(err)
	}
	if got := s.clusterStateSnapshot().epoch; got != 0 {
		t.Fatalf("epoch=%d want=0 after no-op", got)
	}

	if _, err := s.executeClusterSetSlot([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"), []byte("9999"),
		[]byte("MIGRATING"), []byte(clusterNodeID(b)),
	}); err != nil {
		t.Fatal(err)
	}
	if got := s.clusterStateSnapshot().epoch; got != 1 {
		t.Fatalf("epoch=%d want=1 after topology change", got)
	}

	if _, err := s.executeClusterSetSlot([][]byte{
		[]byte("CLUSTER"), []byte("SETSLOT"), []byte("9999"),
		[]byte("MIGRATING"), []byte(clusterNodeID(b)),
	}); err != nil {
		t.Fatal(err)
	}
	if got := s.clusterStateSnapshot().epoch; got != 1 {
		t.Fatalf("epoch=%d want=1 after repeated no-op", got)
	}
}
