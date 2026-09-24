package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"snugkv/internal/engine"
)

func TestReplicationPersistenceCheckpointAndClear(t *testing.T) {
	s := New(engine.New())
	tcp := &TCPServer{server: s}
	path := filepath.Join(t.TempDir(), "appendonly.aof.replication")
	replicationPersistencePaths.Store(s, path)
	defer replicationPersistencePaths.Delete(s)

	runID := strings.Repeat("a", 40)
	s.replication.mu.Lock()
	s.replication.role = replicationReplica
	s.replication.masterHost = "127.0.0.1"
	s.replication.masterPort = 6398
	s.replication.masterRunID = runID
	s.replication.offset = 12345
	s.replication.masterRedisStream = true
	s.replication.mu.Unlock()

	if err := tcp.CheckpointReplicationPersistence(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got replicationPersistenceState
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != replicationPersistenceVersion ||
		got.MasterHost != "127.0.0.1" ||
		got.MasterPort != 6398 ||
		got.MasterRunID != runID ||
		got.Offset != 12345 ||
		!got.RedisStream {
		t.Fatalf("unexpected checkpoint: %+v", got)
	}

	s.replication.promote()
	if err := tcp.CheckpointReplicationPersistence(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("checkpoint still exists after promotion: %v", err)
	}
}

func TestConfigureReplicationPersistenceRestoresContinuation(t *testing.T) {
	s := New(engine.New())
	tcp := &TCPServer{server: s}
	defer s.stopReplicaFollow()

	aof := filepath.Join(t.TempDir(), "appendonly.aof")
	path := aof + ".replication"
	runID := strings.Repeat("b", 40)
	payload, err := json.Marshal(replicationPersistenceState{
		Version:     replicationPersistenceVersion,
		MasterHost:  "127.0.0.1",
		MasterPort:  1,
		MasterRunID: runID,
		Offset:      9876,
		RedisStream: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	defer replicationPersistencePaths.Delete(s)

	if err := tcp.ConfigureReplicationPersistence(aof, ""); err != nil {
		t.Fatal(err)
	}

	s.replication.mu.RLock()
	defer s.replication.mu.RUnlock()
	if s.replication.role != replicationReplica ||
		s.replication.masterHost != "127.0.0.1" ||
		s.replication.masterPort != 1 ||
		s.replication.masterRunID != runID ||
		s.replication.offset != 9876 ||
		!s.replication.masterRedisStream {
		t.Fatalf(
			"unexpected restored state role=%v host=%q port=%d runid=%q offset=%d redis=%v",
			s.replication.role,
			s.replication.masterHost,
			s.replication.masterPort,
			s.replication.masterRunID,
			s.replication.offset,
			s.replication.masterRedisStream,
		)
	}
}

func TestConfigureReplicationPersistenceRejectsCorruptState(t *testing.T) {
	s := New(engine.New())
	tcp := &TCPServer{server: s}

	aof := filepath.Join(t.TempDir(), "appendonly.aof")
	path := aof + ".replication"
	if err := os.WriteFile(path, []byte(`{"version":1,"master_host":"127.0.0.1","master_port":6398,"master_run_id":"bad","offset":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	defer replicationPersistencePaths.Delete(s)

	if err := tcp.ConfigureReplicationPersistence(aof, ""); err == nil {
		t.Fatal("expected corrupt replication checkpoint to fail")
	}
}
