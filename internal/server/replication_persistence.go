package server

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"snugkv/internal/persistence"
	"sync"
)

const replicationPersistenceVersion = 1

type replicationPersistenceState struct {
	Version     int    `json:"version"`
	MasterHost  string `json:"master_host"`
	MasterPort  int    `json:"master_port"`
	MasterRunID string `json:"master_run_id"`
	Offset      int64  `json:"offset"`
	RedisStream bool   `json:"redis_stream"`
}

var replicationPersistencePaths sync.Map // map[*Server]string

func replicationPersistencePath(aofPath, snapshotPath string) string {
	if aofPath != "" {
		return aofPath + ".replication"
	}
	if snapshotPath != "" {
		return snapshotPath + ".replication"
	}
	return ""
}

// ConfigureReplicationPersistence restores a previously checkpointed upstream
// PSYNC continuation tuple and resumes following that upstream. The checkpoint
// is written only after the keyspace has been made durable during graceful
// shutdown, so its offset never intentionally advances beyond recoverable data.
func (s *TCPServer) ConfigureReplicationPersistence(aofPath, snapshotPath string) error {
	return s.ConfigureReplicationPersistenceRecovered(aofPath, snapshotPath, nil)
}

// ConfigureReplicationPersistenceRecovered prefers a crash-safe checkpoint
// recovered from the logical persistence stream. The graceful-shutdown sidecar
// remains the fallback for compacted AOF/snapshot shutdown checkpoints.
func (s *TCPServer) ConfigureReplicationPersistenceRecovered(
	aofPath, snapshotPath string,
	recovered *persistence.ReplicationCheckpoint,
) error {
	path := replicationPersistencePath(aofPath, snapshotPath)
	if path == "" {
		return nil
	}
	replicationPersistencePaths.Store(s.server, path)

	var state replicationPersistenceState
	if recovered != nil {
		state = replicationPersistenceState{
			Version:     replicationPersistenceVersion,
			MasterHost:  recovered.MasterHost,
			MasterPort:  recovered.MasterPort,
			MasterRunID: recovered.MasterRunID,
			Offset:      recovered.Offset,
			RedisStream: recovered.RedisStream,
		}
	} else {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("replication recovery: %w", err)
		}
		if err := json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("replication recovery: %w", err)
		}
	}

	if state.Version != replicationPersistenceVersion {
		return fmt.Errorf("replication recovery: unsupported state version %d", state.Version)
	}
	if state.MasterHost == "" || state.MasterPort <= 0 || state.MasterPort > 65535 {
		return fmt.Errorf("replication recovery: invalid upstream")
	}
	if state.Offset < 0 || len(state.MasterRunID) != 40 {
		return fmt.Errorf("replication recovery: invalid PSYNC state")
	}
	if _, err := hex.DecodeString(state.MasterRunID); err != nil {
		return fmt.Errorf("replication recovery: invalid master run ID")
	}

	s.server.replication.mu.Lock()
	s.server.replication.masterHost = state.MasterHost
	s.server.replication.masterPort = state.MasterPort
	s.server.replication.masterRunID = state.MasterRunID
	s.server.replication.offset = state.Offset
	s.server.replication.masterRedisStream = state.RedisStream
	s.server.replication.mu.Unlock()

	s.server.startReplicaFollow(state.MasterHost, state.MasterPort)
	return nil
}

func (s *Server) replicationCheckpointForOffset(offset int64, redisStream bool) *persistence.ReplicationCheckpoint {
	s.replication.mu.RLock()
	defer s.replication.mu.RUnlock()
	if s.replication.role != replicationReplica ||
		s.replication.masterHost == "" ||
		s.replication.masterRunID == "" {
		return nil
	}
	return &persistence.ReplicationCheckpoint{
		MasterHost:  s.replication.masterHost,
		MasterPort:  s.replication.masterPort,
		MasterRunID: s.replication.masterRunID,
		Offset:      offset,
		RedisStream: redisStream,
	}
}

func (s *Server) persistReplicationCheckpointClearLocked() error {
	if s.journal == nil {
		return nil
	}
	return s.journal.Append([]persistence.Record{{
		Replication: &persistence.ReplicationCheckpoint{Clear: true},
	}})
}

// HasReplicationContinuationState reports whether a graceful shutdown should
// make the current replica dataset durable before checkpointing PSYNC state.
func (s *TCPServer) HasReplicationContinuationState() bool {
	s.server.replication.mu.RLock()
	defer s.server.replication.mu.RUnlock()
	return s.server.replication.role == replicationReplica &&
		s.server.replication.masterHost != "" &&
		s.server.replication.masterRunID != ""
}

// CheckpointReplicationPersistence atomically saves the continuation tuple.
// Call this only after the current keyspace has been durably checkpointed.
func (s *TCPServer) CheckpointReplicationPersistence() error {
	value, ok := replicationPersistencePaths.Load(s.server)
	if !ok {
		return nil
	}
	path := value.(string)

	s.server.replication.mu.RLock()
	if s.server.replication.role != replicationReplica ||
		s.server.replication.masterHost == "" ||
		s.server.replication.masterRunID == "" {
		s.server.replication.mu.RUnlock()
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	state := replicationPersistenceState{
		Version:     replicationPersistenceVersion,
		MasterHost:  s.server.replication.masterHost,
		MasterPort:  s.server.replication.masterPort,
		MasterRunID: s.server.replication.masterRunID,
		Offset:      s.server.replication.offset,
		RedisStream: s.server.replication.masterRedisStream,
	}
	s.server.replication.mu.RUnlock()

	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeSidecarStateAtomic(path, payload)
}

func (s *Server) clearReplicationPersistence() error {
	value, ok := replicationPersistencePaths.Load(s)
	if !ok {
		return nil
	}
	path := value.(string)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
